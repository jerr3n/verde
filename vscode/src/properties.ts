import { randomBytes } from "node:crypto";
import * as vscode from "vscode";
import { LiveProject } from "./live";
import { buildPanel } from "./panel";
import { Ref } from "./protocol";
import { applyEdit } from "./values";

// The Properties view. The page in media/ only draws what it's sent and reports edits,
// which are checked here against the instance's current values.
export class PropertiesProvider implements vscode.WebviewViewProvider {
  private view: vscode.WebviewView | undefined;
  private selected: Ref | undefined;

  constructor(
    private readonly extensionUri: vscode.Uri,
    private readonly live: LiveProject,
    private readonly emptyMessage: () => string,
  ) {}

  select(id: Ref | undefined): void {
    this.selected = id;
    this.post(true);
  }

  // force redraws even if the user is typing in a field, which otherwise waits until they finish.
  post(force = false): void {
    const panel = this.selected === undefined ? undefined : buildPanel(this.live.store, this.selected);
    void this.view?.webview.postMessage({ type: "state", panel, message: this.emptyMessage(), force });
  }

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    const media = vscode.Uri.joinPath(this.extensionUri, "media");
    view.webview.options = { enableScripts: true, localResourceRoots: [media] };
    view.webview.html = this.html(view.webview, media);
    view.webview.onDidReceiveMessage((message) => this.receive(message));
    view.onDidDispose(() => {
      this.view = undefined;
    });
  }

  private receive(message: { type?: string; id?: unknown; name?: unknown; input?: unknown }): void {
    switch (message.type) {
      case "ready":
        this.post(true);
        break;
      case "open":
        void vscode.commands.executeCommand("verde.openInstance", message.id);
        break;
      case "edit":
        if (typeof message.id === "string" && typeof message.name === "string") {
          this.edit(message.id, message.name, message.input);
        }
        break;
    }
  }

  private edit(id: Ref, name: string, input: unknown): void {
    const { store } = this.live;
    const old = store.get(id)?.props?.[name];
    if (!old || store.isMount(id) || (name === "Source" && store.isScript(id))) {
      return;
    }
    const result = applyEdit(old, input);
    if ("error" in result) {
      void vscode.window.showWarningMessage(`${name}: ${result.error}`);
      this.post(true); // puts back the value that's really there
      return;
    }
    if (JSON.stringify(result.value) !== JSON.stringify(old)) {
      this.live.setProperty(id, name, result.value);
    }
  }

  private html(webview: vscode.Webview, media: vscode.Uri): string {
    const nonce = randomBytes(16).toString("hex");
    const script = webview.asWebviewUri(vscode.Uri.joinPath(media, "properties.js"));
    const style = webview.asWebviewUri(vscode.Uri.joinPath(media, "properties.css"));
    return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src ${webview.cspSource}; script-src 'nonce-${nonce}';">
<link rel="stylesheet" href="${style}">
</head>
<body>
<main id="root"></main>
<script nonce="${nonce}" src="${script}"></script>
</body>
</html>`;
  }
}
