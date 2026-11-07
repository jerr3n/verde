import * as fs from "node:fs";
import * as net from "node:net";
import * as os from "node:os";
import * as path from "node:path";

export const BINARY = path.join(__dirname, "verde");

export function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.on("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address() as net.AddressInfo;
      probe.close(() => resolve(port));
    });
  });
}

export async function makeProject(files: Record<string, string>, extra = ""): Promise<{ dir: string; port: number }> {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "verde-vscode-"));
  process.on("exit", () => fs.rmSync(dir, { recursive: true, force: true }));
  const port = await freePort();
  fs.writeFileSync(
    path.join(dir, "project.yml"),
    `version: 0\nname: test\nserve:\n  port: ${port}\n${extra}sync:\n  - instance: Workspace\n    path: src/workspace\n  - instance: StarterPlayer/StarterPlayerScripts\n    path: src/local\n`,
  );
  for (const [name, text] of Object.entries(files)) {
    const full = path.join(dir, name);
    fs.mkdirSync(path.dirname(full), { recursive: true });
    fs.writeFileSync(full, text);
  }
  return { dir, port };
}

export async function waitFor<T>(what: string, check: () => T | undefined | false): Promise<T> {
  const deadline = Date.now() + 5000;
  for (;;) {
    const result = check();
    if (result) {
      return result;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}
