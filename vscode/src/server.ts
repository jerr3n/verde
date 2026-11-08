// SPDX-License-Identifier: MPL-2.0

import { ChildProcess, spawn } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { LogLine, parseLogLine, wsUrl } from "./logs";

export type ServerState =
  | { kind: "stopped" }
  | { kind: "starting" }
  | { kind: "running"; url: string }
  | { kind: "failed"; message: string };

export interface ServerEvents {
  onState(state: ServerState): void;
  onLog(log: LogLine): void;
}

// Finds the executable, as an absolute path. Spawning a bare name could run a file from the
// workspace, since Windows searches the working directory first.
export function findBinary(
  configured: string,
  env: NodeJS.ProcessEnv = process.env,
  platform: NodeJS.Platform = process.platform,
  exists: (file: string) => boolean = isExecutable,
): string | undefined {
  if (configured) {
    return exists(configured) ? configured : undefined;
  }
  const name = platform === "win32" ? "verde.exe" : "verde";
  const separator = platform === "win32" ? ";" : ":";
  const dirs = (env.PATH ?? env.Path ?? "").split(separator).filter((dir) => path.isAbsolute(dir));
  const gopath = env.GOPATH?.split(separator)[0] || path.join(env.HOME ?? env.USERPROFILE ?? os.homedir(), "go");
  dirs.push(path.join(gopath, "bin"));
  return dirs.map((dir) => path.join(dir, name)).find(exists);
}

function isExecutable(file: string): boolean {
  try {
    fs.accessSync(file, fs.constants.X_OK);
    return fs.statSync(file).isFile();
  } catch {
    return false;
  }
}

const KILL_AFTER_MS = 3000;

// Runs `verde serve`, and works out its state from its output and exit.
export class VerdeServer {
  private child: ChildProcess | undefined;
  private stopped: Promise<void> | undefined;
  private lastError = "";
  private stopRequested = false;
  private current: ServerState = { kind: "stopped" };

  constructor(private readonly events: ServerEvents) {}

  get state(): ServerState {
    return this.current;
  }

  start(binary: string, cwd: string): void {
    if (this.child) {
      return;
    }
    this.lastError = "";
    this.setState({ kind: "starting" });

    // Release mode makes the logs JSON, so nothing here depends on how they're worded for people.
    const child = spawn(binary, ["serve"], {
      cwd,
      env: { ...process.env, GIN_MODE: "release" },
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    this.child = child;

    let finish: () => void;
    this.stopped = new Promise((resolve) => (finish = resolve));

    readLines(child.stdout!, (line) => this.handleLine(line));
    readLines(child.stderr!, (line) => this.handleLine(line));

    // An "error" can come without a "close", and a failed spawn only has the "error".
    let done = false;
    const exited = (message: string) => {
      if (done) {
        return;
      }
      done = true;
      this.child = undefined;
      this.setState(this.stopRequested ? { kind: "stopped" } : { kind: "failed", message });
      this.stopRequested = false;
      finish();
    };
    child.on("error", (err) => exited(err.message));
    // "close" rather than "exit", so that the output has all been read by then.
    child.on("close", (code, signal) => {
      exited(this.lastError || `The server exited unexpectedly (${signal ?? `code ${code}`}).`);
    });
  }

  async stop(): Promise<void> {
    const child = this.child;
    if (!child) {
      return;
    }
    this.stopRequested = true;
    child.kill();
    // It doesn't handle signals, and writes files atomically, so nothing's lost by this.
    const force = setTimeout(() => child.kill("SIGKILL"), KILL_AFTER_MS);
    await this.stopped;
    clearTimeout(force);
  }

  private handleLine(line: string): void {
    const log = parseLogLine(line);
    if (log.level === "text") {
      const error = /^error: (.*)$/.exec(line);
      if (error) {
        this.lastError = error[1];
      }
    } else if (log.message === "listening" && typeof log.fields.addr === "string") {
      this.setState({ kind: "running", url: wsUrl(log.fields.addr) });
    }
    this.events.onLog(log);
  }

  private setState(state: ServerState): void {
    this.current = state;
    this.events.onState(state);
  }
}

function readLines(stream: NodeJS.ReadableStream, onLine: (line: string) => void): void {
  let buffered = "";
  stream.setEncoding("utf8");
  stream.on("data", (chunk: string) => {
    buffered += chunk;
    const lines = buffered.split(/\r?\n/);
    buffered = lines.pop() ?? "";
    for (const line of lines) {
      if (line) {
        onLine(line);
      }
    }
  });
  stream.on("end", () => {
    if (buffered) {
      onLine(buffered);
    }
  });
}
