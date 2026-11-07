import { Located, Patch, PROTOCOL_VERSION, Ref, Snapshot, Value } from "./protocol";
import { InstanceStore } from "./store";

const RETRY_MS = 250;
const LOCATE_TIMEOUT_MS = 5000;
const CLIENT = "verde-vscode/0.1.0";

export interface LiveEvents {
  onChange(): void; // the tree changed
  onPlugins(count: number): void; // connected Studios
  onProblem(message: string): void;
}

// A connection to the server as an editor client. It keeps `store` in step with the server,
// reconnects while the server is up, and turns edits into patches. Connecting changes
// nothing, since only a plugin decides who wins, so reconnecting on its own is safe here.
export class LiveProject {
  readonly store = new InstanceStore();

  private url: string | undefined;
  private socket: WebSocket | undefined;
  private retry: NodeJS.Timeout | undefined;
  private seq = 0;
  private hasSnapshot = false;
  private refused = false;
  private readonly pending = new Map<number, { resolve(located: Located): void; reject(err: Error): void }>();

  constructor(private readonly events: LiveEvents) {}

  connect(url: string): void {
    this.disconnect();
    this.url = url;
    this.open();
  }

  disconnect(): void {
    this.url = undefined;
    this.refused = false;
    clearTimeout(this.retry);
    const socket = this.socket;
    this.socket = undefined;
    socket?.close();
    this.reset();
  }

  setProperty(id: Ref, name: string, value: Value): void {
    this.send({ updated: [{ id, props: { [name]: value } }] });
  }

  rename(id: Ref, name: string): void {
    this.send({ updated: [{ id, name }] });
  }

  remove(id: Ref): void {
    this.send({ removed: [id] });
  }

  locate(id: Ref): Promise<Located> {
    return new Promise((resolve, reject) => {
      const seq = this.write("locate", { id });
      if (seq === undefined) {
        reject(new Error("The server isn't connected."));
        return;
      }
      const timeout = setTimeout(() => {
        this.pending.delete(seq);
        reject(new Error("The server didn't answer."));
      }, LOCATE_TIMEOUT_MS);
      this.pending.set(seq, {
        resolve: (located) => {
          clearTimeout(timeout);
          resolve(located);
        },
        reject: (err) => {
          clearTimeout(timeout);
          reject(err);
        },
      });
    });
  }

  // The server doesn't echo a sender's patch back, so it's applied here too. If it fails,
  // the error handler below gets the server's version.
  private send(patch: Patch): void {
    if (this.write("patch", patch) === undefined) {
      this.events.onProblem("The server isn't connected, so that change wasn't made.");
      return;
    }
    this.store.apply(patch);
    this.events.onChange();
  }

  private write(type: string, data: unknown): number | undefined {
    if (!this.hasSnapshot || this.socket?.readyState !== WebSocket.OPEN) {
      return undefined;
    }
    this.seq += 1;
    this.socket.send(JSON.stringify({ type, seq: this.seq, data }));
    return this.seq;
  }

  private open(): void {
    if (!this.url) {
      return;
    }
    const socket = new WebSocket(this.url);
    this.socket = socket;
    this.seq = 0;

    socket.addEventListener("open", () => {
      this.seq = 1;
      socket.send(
        JSON.stringify({
          type: "hello",
          seq: 1,
          data: { protocolVersion: PROTOCOL_VERSION, client: CLIENT, placeId: 0, role: "editor" },
        }),
      );
    });
    socket.addEventListener("message", (event) => {
      if (socket === this.socket && typeof event.data === "string") {
        this.handle(event.data);
      }
    });
    socket.addEventListener("close", () => {
      if (socket !== this.socket) {
        return;
      }
      this.socket = undefined;
      this.reset();
      if (this.url && !this.refused) {
        this.retry = setTimeout(() => this.open(), RETRY_MS);
      }
    });
    // A failed connection also fires "close", which is what retries.
    socket.addEventListener("error", () => {});
  }

  private reset(): void {
    this.hasSnapshot = false;
    for (const { reject } of this.pending.values()) {
      reject(new Error("The connection to the server closed."));
    }
    this.pending.clear();
    if (this.store.root !== undefined) {
      this.store.clear();
      this.events.onChange();
    }
    this.events.onPlugins(0);
  }

  private handle(text: string): void {
    let message: { type?: string; data?: unknown };
    try {
      message = JSON.parse(text);
    } catch {
      return;
    }
    switch (message.type) {
      case "snapshot":
        this.store.load(message.data as Snapshot);
        this.hasSnapshot = true;
        this.events.onChange();
        break;
      case "patch":
        this.store.apply(message.data as Patch);
        this.events.onChange();
        break;
      case "status":
        this.events.onPlugins((message.data as { plugins: number }).plugins);
        break;
      case "located": {
        const located = message.data as Located;
        this.pending.get(located.seq)?.resolve(located);
        this.pending.delete(located.seq);
        break;
      }
      case "error":
        this.handleError(message.data as { seq?: number; message: string });
        break;
    }
  }

  private handleError(error: { seq?: number; message: string }): void {
    const pending = error.seq === undefined ? undefined : this.pending.get(error.seq);
    if (pending) {
      this.pending.delete(error.seq!);
      pending.reject(new Error(error.message));
      return;
    }
    this.events.onProblem(error.message);
    if (!this.hasSnapshot) {
      // Refused, so trying again would only say the same thing.
      this.refused = true;
      return;
    }
    // Something in an edit didn't apply, so the local copy may be wrong. A new snapshot is
    // the simple way to get the truth, and it's harmless.
    this.socket?.close();
  }
}
