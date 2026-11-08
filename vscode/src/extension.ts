// SPDX-License-Identifier: MPL-2.0

import { execFile } from "node:child_process";
import * as path from "node:path";
import * as vscode from "vscode";
import { ExplorerProvider } from "./explorer";
import { LiveProject } from "./live";
import { formatLogLine, isNoise, LogLine, problemFrom } from "./logs";
import { looksLikeVerdeConfig } from "./project";
import { PropertiesProvider } from "./properties";
import { Ref } from "./protocol";
import { findBinary, ServerState, VerdeServer } from "./server";

const CONFIG_NAME = "project.yml";
const PROJECT_KEY = "verde.project";
const SAME_PROBLEM_MS = 30_000;

let app: VerdeApp | undefined;

export function activate(context: vscode.ExtensionContext): void {
  app = new VerdeApp(context);
  void app.startup();
}

export async function deactivate(): Promise<void> {
  await app?.shutdown();
}

class VerdeApp {
  private readonly output = vscode.window.createOutputChannel("Verde");
  private readonly status = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 50);
  private readonly server = new VerdeServer({
    onState: (state) => this.onServerState(state),
    onLog: (log) => this.onServerLog(log),
  });
  private readonly live = new LiveProject({
    onChange: () => {
      this.explorer.refresh();
      this.properties.post();
    },
    onPlugins: (count) => {
      this.plugins = count;
      this.updateStatus();
    },
    onProblem: (message) => this.showProblem(message),
  });
  private readonly explorer = new ExplorerProvider(this.live.store);
  private readonly properties: PropertiesProvider;
  private readonly treeView: vscode.TreeView<Ref>;

  private projects: vscode.Uri[] = [];
  private running: vscode.Uri | undefined;
  private plugins = 0;
  private restartPromptOpen = false;
  private readonly recentProblems = new Map<string, number>();

  constructor(private readonly context: vscode.ExtensionContext) {
    this.properties = new PropertiesProvider(context.extensionUri, this.live, () => this.emptyMessage());
    this.treeView = vscode.window.createTreeView("verde.explorer", {
      treeDataProvider: this.explorer,
      showCollapseAll: true,
    });
    this.status.command = "verde.menu";

    const watcher = vscode.workspace.createFileSystemWatcher(`**/${CONFIG_NAME}`);
    context.subscriptions.push(
      this.output,
      this.status,
      this.treeView,
      watcher,
      this.treeView.onDidChangeSelection((event) => this.properties.select(event.selection[0])),
      vscode.window.registerWebviewViewProvider("verde.properties", this.properties),
      watcher.onDidCreate(() => this.discover()),
      watcher.onDidDelete(() => this.discover()),
      watcher.onDidChange((uri) => this.onConfigChanged(uri)),
      vscode.workspace.onDidChangeWorkspaceFolders(() => this.discover()),
    );

    const commands: Record<string, (...args: never[]) => unknown> = {
      "verde.start": () => this.start(),
      "verde.stop": () => this.server.stop(),
      "verde.restart": () => this.restart(),
      "verde.init": () => this.init(),
      "verde.showLogs": () => this.output.show(),
      "verde.copyUrl": () => this.copyUrl(),
      "verde.openConfig": () => this.openConfig(),
      "verde.menu": () => this.menu(),
      "verde.openInstance": (id?: Ref) => this.openInstance(id ?? this.treeView.selection[0]),
      "verde.renameInstance": (id?: Ref) => this.renameInstance(id ?? this.treeView.selection[0]),
      "verde.deleteInstance": (id?: Ref) => this.deleteInstance(id ?? this.treeView.selection[0]),
    };
    for (const [name, run] of Object.entries(commands)) {
      context.subscriptions.push(vscode.commands.registerCommand(name, run));
    }
  }

  async startup(): Promise<void> {
    this.onServerState(this.server.state);
    await this.discover();
    if (this.projects.length > 0 && vscode.workspace.getConfiguration("verde").get<boolean>("autoStart")) {
      await this.start();
    }
  }

  async shutdown(): Promise<void> {
    this.live.disconnect();
    await this.server.stop();
  }

  // Finding the project

  private async discover(): Promise<void> {
    const found = await vscode.workspace.findFiles(`**/${CONFIG_NAME}`, "{**/node_modules/**,**/.git/**,**/.verde/**}", 50);
    const projects: vscode.Uri[] = [];
    for (const uri of found) {
      try {
        const text = Buffer.from(await vscode.workspace.fs.readFile(uri)).toString("utf8");
        if (looksLikeVerdeConfig(text)) {
          projects.push(uri);
        }
      } catch {
        // It went away while looking at it.
      }
    }
    this.projects = projects.sort((a, b) => a.fsPath.split(path.sep).length - b.fsPath.split(path.sep).length);
    await vscode.commands.executeCommand("setContext", "verde.hasProject", projects.length > 0);
    this.updateStatus();
  }

  private savedProject(): vscode.Uri | undefined {
    const saved = this.context.workspaceState.get<string>(PROJECT_KEY);
    return this.projects.find((uri) => uri.toString() === saved);
  }

  private async chooseProject(): Promise<vscode.Uri | undefined> {
    if (this.projects.length <= 1) {
      return this.projects[0];
    }
    const saved = this.savedProject();
    if (saved) {
      return saved;
    }
    const picked = await vscode.window.showQuickPick(
      this.projects.map((uri) => ({ label: vscode.workspace.asRelativePath(uri), uri })),
      { placeHolder: "Which Verde project?" },
    );
    if (picked) {
      await this.context.workspaceState.update(PROJECT_KEY, picked.uri.toString());
    }
    return picked?.uri;
  }

  // The server

  private binary(): string | undefined {
    const configured = vscode.workspace.getConfiguration("verde").get<string>("path") ?? "";
    const binary = findBinary(configured);
    if (!binary) {
      const where = configured ? `at ${configured}` : "on PATH or in $GOPATH/bin";
      void vscode.window
        .showErrorMessage(`Couldn't find the verde executable ${where}. Run \`make install\` in the Verde repo, or set verde.path.`, "Open Settings")
        .then((choice) => {
          if (choice) {
            void vscode.commands.executeCommand("workbench.action.openSettings", "verde.path");
          }
        });
    }
    return binary;
  }

  private async start(): Promise<void> {
    const { kind } = this.server.state;
    if (kind === "running" || kind === "starting") {
      return;
    }
    const project = await this.chooseProject();
    if (!project) {
      if (this.projects.length === 0) {
        void vscode.window.showInformationMessage(`There's no Verde ${CONFIG_NAME} in this workspace.`, "Create Project").then((choice) => {
          if (choice) {
            void this.init();
          }
        });
      }
      return;
    }
    const binary = this.binary();
    if (!binary) {
      return;
    }
    this.running = project;
    this.output.appendLine(`Starting ${binary} serve in ${path.dirname(project.fsPath)}`);
    this.server.start(binary, path.dirname(project.fsPath));
  }

  private async restart(): Promise<void> {
    await this.server.stop();
    await this.start();
  }

  private onServerState(state: ServerState): void {
    void vscode.commands.executeCommand("setContext", "verde.serverState", state.kind);
    if (state.kind === "running") {
      this.live.connect(state.url);
    } else {
      this.live.disconnect();
    }
    if (state.kind === "failed") {
      this.output.appendLine(`The server stopped: ${state.message}`);
      const hint = /address already in use|only one usage/i.test(state.message) ? ". Is another Verde server running?" : "";
      void vscode.window.showErrorMessage(`Verde: ${state.message}${hint}`, "Show Logs").then((choice) => {
        if (choice) {
          this.output.show();
        }
      });
    }
    this.updateStatus();
    this.properties.post(true);
  }

  private onServerLog(log: LogLine): void {
    if (!isNoise(log)) {
      this.output.appendLine(formatLogLine(log));
    }
    const problem = problemFrom(log);
    if (problem) {
      this.showProblem(problem);
    }
  }

  private showProblem(message: string): void {
    const now = Date.now();
    if (now - (this.recentProblems.get(message) ?? 0) < SAME_PROBLEM_MS) {
      return;
    }
    this.recentProblems.set(message, now);
    this.output.appendLine(message);
    void vscode.window.showWarningMessage(message, "Show Logs").then((choice) => {
      if (choice) {
        this.output.show();
      }
    });
  }

  private async onConfigChanged(uri: vscode.Uri): Promise<void> {
    await this.discover();
    if (uri.toString() !== this.running?.toString() || this.server.state.kind !== "running" || this.restartPromptOpen) {
      return;
    }
    this.restartPromptOpen = true;
    const choice = await vscode.window.showInformationMessage(`${CONFIG_NAME} changed. Restart the server to use it?`, "Restart");
    this.restartPromptOpen = false;
    if (choice) {
      await this.restart();
    }
  }

  // Status bar and menu

  private updateStatus(): void {
    const state = this.server.state;
    if (this.projects.length === 0 && state.kind === "stopped") {
      this.status.hide();
      return;
    }
    this.status.backgroundColor = undefined;
    switch (state.kind) {
      case "stopped":
        this.status.text = "$(debug-start) Verde";
        this.status.tooltip = "The Verde server isn't running. Click for options.";
        break;
      case "starting":
        this.status.text = "$(sync~spin) Verde";
        this.status.tooltip = "The Verde server is starting.";
        break;
      case "running":
        this.status.text = this.plugins > 0 ? `$(plug) Verde: ${studios(this.plugins)}` : "$(radio-tower) Verde: waiting for Studio";
        this.status.tooltip = `Serving at ${state.url}. Click for options.`;
        break;
      case "failed":
        this.status.text = "$(error) Verde";
        this.status.tooltip = state.message;
        this.status.backgroundColor = new vscode.ThemeColor("statusBarItem.errorBackground");
        break;
    }
    this.treeView.description = state.kind === "running" && this.plugins > 0 ? studios(this.plugins) : undefined;
    this.status.show();
  }

  private async menu(): Promise<void> {
    const { kind } = this.server.state;
    const running = kind === "running";
    const items: { label: string; command: string }[] = [
      ...(running || kind === "starting"
        ? [{ label: "$(debug-stop) Stop Server", command: "verde.stop" }]
        : [{ label: "$(debug-start) Start Server", command: "verde.start" }]),
      ...(running
        ? [
            { label: "$(debug-restart) Restart Server", command: "verde.restart" },
            { label: "$(clippy) Copy Server Address", command: "verde.copyUrl" },
          ]
        : []),
      { label: "$(output) Show Logs", command: "verde.showLogs" },
      { label: "$(gear) Open project.yml", command: "verde.openConfig" },
    ];
    const picked = await vscode.window.showQuickPick(items, { placeHolder: "Verde" });
    if (picked) {
      await vscode.commands.executeCommand(picked.command);
    }
  }

  private async copyUrl(): Promise<void> {
    const state = this.server.state;
    if (state.kind === "running") {
      await vscode.env.clipboard.writeText(state.url);
      void vscode.window.setStatusBarMessage(`Copied ${state.url}`, 3000);
    }
  }

  private async openConfig(): Promise<void> {
    const project = this.running ?? (await this.chooseProject());
    if (project) {
      await vscode.window.showTextDocument(project);
    }
  }

  private emptyMessage(): string {
    return this.server.state.kind === "running"
      ? "Select an instance in the Explorer to see its properties."
      : "Start the server to see properties.";
  }

  // Creating a project

  private async init(): Promise<void> {
    const folders = vscode.workspace.workspaceFolders ?? [];
    if (folders.length === 0) {
      void vscode.window.showInformationMessage("Open a folder first, then create the Verde project in it.");
      return;
    }
    const folder = folders.length === 1 ? folders[0] : await vscode.window.showWorkspaceFolderPick({ placeHolder: "Create the project in which folder?" });
    if (!folder) {
      return;
    }
    const binary = this.binary();
    if (!binary) {
      return;
    }
    execFile(binary, ["init"], { cwd: folder.uri.fsPath }, async (err, _stdout, stderr) => {
      if (err) {
        void vscode.window.showErrorMessage(`Verde: ${stderr.replace(/^error: /, "").trim() || err.message}`);
        return;
      }
      await this.discover();
      const choice = await vscode.window.showInformationMessage(`Created ${CONFIG_NAME} and its sync folders.`, "Start Server");
      if (choice) {
        await this.start();
      }
    });
  }

  // Explorer actions

  private async openInstance(id: Ref | undefined): Promise<void> {
    const project = this.running;
    if (id === undefined || !project) {
      return;
    }
    const name = this.live.store.get(id)?.name ?? "That instance";
    try {
      const located = await this.live.locate(id);
      const target = located.file ?? located.props;
      const root = vscode.Uri.joinPath(project, "..");
      if (target) {
        await vscode.window.showTextDocument(vscode.Uri.joinPath(root, target), { preview: true, preserveFocus: true });
      } else if (located.path) {
        await vscode.commands.executeCommand("revealInExplorer", vscode.Uri.joinPath(root, located.path));
      } else {
        void vscode.window.showInformationMessage(`${name} has no file on disk.`);
      }
    } catch (err) {
      void vscode.window.showWarningMessage(`Couldn't open ${name}: ${(err as Error).message}`);
    }
  }

  private async renameInstance(id: Ref | undefined): Promise<void> {
    const instance = id === undefined ? undefined : this.live.store.get(id);
    if (!instance || this.live.store.isMount(instance.id)) {
      return;
    }
    const name = await vscode.window.showInputBox({
      title: `Rename ${instance.name}`,
      value: instance.name,
      validateInput: (value) => (value === "" ? "An instance needs a name." : undefined),
    });
    if (name && name !== instance.name && this.live.store.get(instance.id)) {
      this.live.rename(instance.id, name);
    }
  }

  private async deleteInstance(id: Ref | undefined): Promise<void> {
    const instance = id === undefined ? undefined : this.live.store.get(id);
    if (!instance || this.live.store.isMount(instance.id)) {
      return;
    }
    const count = this.live.store.children(instance.id).length;
    const choice = await vscode.window.showWarningMessage(
      `Delete ${instance.name}${count > 0 ? " and everything inside it" : ""}?`,
      { modal: true, detail: "Its files move to .verde/trash, and it's removed from any connected Studio." },
      "Delete",
    );
    if (choice && this.live.store.get(instance.id)) {
      this.live.remove(instance.id);
    }
  }
}

function studios(count: number): string {
  return count === 1 ? "Studio connected" : `${count} Studios connected`;
}
