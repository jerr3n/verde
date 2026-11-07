// `verde serve` logs one JSON object per line with GIN_MODE=release. Anything else, like the
// "error: ..." it prints when it exits, comes through as plain text.

export interface LogLine {
  level: string;
  message: string;
  fields: Record<string, unknown>;
}

const OWN_KEYS = new Set(["level", "time", "msg", "caller"]);

export function parseLogLine(line: string): LogLine {
  if (line.startsWith("{")) {
    try {
      const entry = JSON.parse(line) as Record<string, unknown>;
      if (typeof entry.msg === "string") {
        const fields: Record<string, unknown> = {};
        for (const [key, value] of Object.entries(entry)) {
          if (!OWN_KEYS.has(key)) {
            fields[key] = value;
          }
        }
        return { level: String(entry.level ?? "info"), message: entry.msg, fields };
      }
    } catch {
      // Not one of the logger's lines after all.
    }
  }
  return { level: "text", message: line, fields: {} };
}

export function formatLogLine(log: LogLine): string {
  if (log.level === "text") {
    return log.message;
  }
  const fields = Object.entries(log.fields)
    .map(([key, value]) => `${key}=${typeof value === "string" ? value : JSON.stringify(value)}`)
    .join(" ");
  return `[${log.level}] ${log.message}${fields ? ` ${fields}` : ""}`;
}

// Whether a line is worth showing in the logs at all. There's a "request" for each closed
// connection, which says nothing useful unless it failed.
export function isNoise(log: LogLine): boolean {
  return log.level === "info" && log.message === "request";
}

// Problems the user has to do something about, as a sentence for a notification. Everything
// else only goes to the logs, like a connection dropping, or a bad patch, which the client
// that sent it is told about.
export function problemFrom(log: LogLine): string | undefined {
  const detail = typeof log.fields.error === "string" ? `: ${log.fields.error}` : "";
  switch (log.message) {
    case "rescan failed, keeping current tree":
      return `Verde can't read your files, so changes from disk aren't syncing${detail}`;
    case "apply disk patch":
      return `Verde couldn't apply a change from disk${detail}`;
    case "retry writes":
      return "Verde couldn't save some instances to disk. See the logs for which.";
    case "watcher stopped":
      return `Verde stopped watching your files${detail}`;
    default:
      return undefined;
  }
}

// The address the plugin should connect to. A server listening on every interface can't be
// connected to at that address.
export function wsUrl(listenAddr: string): string {
  const host = listenAddr.replace(/^0\.0\.0\.0(?=:)/, "127.0.0.1").replace(/^\[::\](?=:)/, "[::1]");
  return `ws://${host}/ws`;
}
