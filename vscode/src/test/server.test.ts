import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import * as fs from "node:fs";
import * as http from "node:http";
import { AddressInfo } from "node:net";
import * as path from "node:path";
import { after, test } from "node:test";
import { LiveProject } from "../live";
import { LogLine } from "../logs";
import { ServerState, VerdeServer } from "../server";
import { BINARY, makeProject, waitFor } from "./helpers";

const cleanups: (() => Promise<void> | void)[] = [];
after(async () => {
  for (const cleanup of cleanups) {
    await cleanup();
  }
});

function startServer(dir: string) {
  const states: ServerState[] = [];
  const logs: LogLine[] = [];
  const server = new VerdeServer({
    onState: (state) => states.push(state),
    onLog: (log) => logs.push(log),
  });
  server.start(BINARY, dir);
  cleanups.push(() => server.stop());
  return { server, states, logs };
}

async function running(server: VerdeServer): Promise<string> {
  const state = await waitFor("the server to run", () => server.state.kind === "running" && server.state);
  return (state as { url: string }).url;
}

function connect(url: string) {
  const problems: string[] = [];
  const pluginCounts: number[] = [];
  const live = new LiveProject({
    onChange: () => {},
    onPlugins: (count) => pluginCounts.push(count),
    onProblem: (message) => problems.push(message),
  });
  live.connect(url);
  cleanups.push(() => live.disconnect());
  return { live, problems, pluginCounts };
}

function find(live: LiveProject, target: string, from = live.store.root): string | undefined {
  if (from === undefined) {
    return undefined;
  }
  for (const child of live.store.children(from)) {
    if (live.store.path(child.id) === target) {
      return child.id;
    }
    const found = find(live, target, child.id);
    if (found) {
      return found;
    }
  }
  return undefined;
}

function waitForInstance(live: LiveProject, target: string): Promise<string> {
  return waitFor(`${target} to appear`, () => find(live, target));
}

const read = (dir: string, name: string) => fs.readFileSync(path.join(dir, name), "utf8");

test("the server starts, reports its address, and stops", async () => {
  const { dir, port } = await makeProject({});
  const { server, states } = startServer(dir);
  assert.equal(await running(server), `ws://127.0.0.1:${port}/ws`);
  await server.stop();
  assert.deepEqual(states.map((state) => state.kind), ["starting", "running", "stopped"]);
});

test("a bad config fails with the server's own message", async () => {
  const { dir } = await makeProject({});
  fs.writeFileSync(path.join(dir, "project.yml"), "version: 99\nserve: {port: 1234}\nsync: []\n");
  const { server } = startServer(dir);
  const state = await waitFor("the server to fail", () => server.state.kind === "failed" && server.state);
  assert.match((state as { message: string }).message, /version/);
});

test("a port that's taken fails after looking like it started", async () => {
  const { dir } = await makeProject({});
  const first = startServer(dir);
  await running(first.server);

  const second = startServer(dir);
  const state = await waitFor("the second server to fail", () => second.server.state.kind === "failed" && second.server.state);
  assert.match((state as { message: string }).message, /address already in use/);
  assert.equal(first.server.state.kind, "running");
});

test("a missing executable fails instead of throwing", async () => {
  const { dir } = await makeProject({});
  const server = new VerdeServer({ onState: () => {}, onLog: () => {} });
  server.start(path.join(dir, "no-such-binary"), dir);
  const state = await waitFor("the spawn to fail", () => server.state.kind === "failed" && server.state);
  assert.match((state as { message: string }).message, /ENOENT/);
});

test("the tree comes from the server, in the shape of the game", async () => {
  const { dir } = await makeProject({
    "src/workspace/Main.server.luau": "print('hi')\n",
    "src/workspace/Lamp.Part.inst.json": '{"props":{"Anchored":{"t":"Bool","v":true}}}\n',
    "src/local/Input.client.luau": "",
  });
  const { server } = startServer(dir);
  const { live, pluginCounts } = connect(await running(server));

  await waitForInstance(live, "Workspace/Main");
  assert.equal(live.store.isScript(find(live, "Workspace/Main")!), true);
  assert.deepEqual(live.store.get(find(live, "Workspace/Lamp")!)?.props, { Anchored: { t: "Bool", v: true } });

  const mount = find(live, "StarterPlayer")!;
  assert.equal(live.store.isMount(mount), true);
  assert.equal(live.store.isSyncMount(mount), false);
  assert.equal(live.store.isSyncMount(find(live, "StarterPlayer/StarterPlayerScripts")!), true);

  await waitFor("the first status", () => pluginCounts.length > 0);
  assert.equal(pluginCounts.at(-1), 0);
});

test("the tree follows the disk", async () => {
  const { dir } = await makeProject({ "src/workspace/Main.server.luau": "a" });
  const { server } = startServer(dir);
  const { live } = connect(await running(server));
  await waitForInstance(live, "Workspace/Main");

  fs.writeFileSync(path.join(dir, "src/workspace/Other.luau"), "b");
  await waitForInstance(live, "Workspace/Other");

  fs.rmSync(path.join(dir, "src/workspace/Main.server.luau"));
  await waitFor("Main to go", () => !find(live, "Workspace/Main"));
});

test("editing a property writes it to disk, and survives a restart", async () => {
  const { dir } = await makeProject({
    "src/workspace/Lamp.Part.inst.json": '{"props":{"Anchored":{"t":"Bool","v":true}}}\n',
  });
  const first = startServer(dir);
  const { live, problems } = connect(await running(first.server));
  const lamp = await waitForInstance(live, "Workspace/Lamp");

  live.setProperty(lamp, "Anchored", { t: "Bool", v: false });
  assert.deepEqual(live.store.get(lamp)?.props?.Anchored, { t: "Bool", v: false }, "shown straight away");
  await waitFor("the file to change", () => read(dir, "src/workspace/Lamp.Part.inst.json").includes('"v":false'));

  await first.server.stop();
  await waitFor("the tree to clear", () => live.store.root === undefined);

  const second = startServer(dir);
  await running(second.server);
  const again = await waitForInstance(live, "Workspace/Lamp"); // it reconnected by itself
  assert.deepEqual(live.store.get(again)?.props?.Anchored, { t: "Bool", v: false });
  assert.deepEqual(problems, []);
});

test("renaming moves the files", async () => {
  const { dir } = await makeProject({ "src/workspace/Main.server.luau": "a" });
  const { server } = startServer(dir);
  const { live } = connect(await running(server));
  const main = await waitForInstance(live, "Workspace/Main");

  live.rename(main, "Renamed");
  assert.equal(live.store.path(main), "Workspace/Renamed");
  await waitFor("the file to move", () => fs.existsSync(path.join(dir, "src/workspace/Renamed.server.luau")));
  assert.equal(fs.existsSync(path.join(dir, "src/workspace/Main.server.luau")), false);
});

test("deleting moves the files to the trash", async () => {
  const { dir } = await makeProject({ "src/workspace/lib/Util.luau": "keep me" });
  const { server } = startServer(dir);
  const { live } = connect(await running(server));
  const lib = await waitForInstance(live, "Workspace/lib");
  await waitForInstance(live, "Workspace/lib/Util");

  live.remove(lib);
  assert.equal(find(live, "Workspace/lib/Util"), undefined);
  await waitFor("the folder to go", () => !fs.existsSync(path.join(dir, "src/workspace/lib")));

  const trashed = fs.readdirSync(path.join(dir, ".verde/trash"), { recursive: true }).map(String);
  assert.ok(trashed.some((name) => name.endsWith(path.join("lib", "Util.luau"))), trashed.join());
});

test("locate finds an instance's files", async () => {
  const { dir } = await makeProject({
    "src/workspace/Main.server.luau": "a",
    "src/workspace/Lamp.Part.inst.json": "{}\n",
    "src/workspace/lib/Util.luau": "b",
  });
  const { server } = startServer(dir);
  const { live } = connect(await running(server));
  await waitForInstance(live, "Workspace/lib/Util");

  assert.equal((await live.locate(find(live, "Workspace/Main")!)).file, "src/workspace/Main.server.luau");
  assert.equal((await live.locate(find(live, "Workspace/Lamp")!)).props, "src/workspace/Lamp.Part.inst.json");
  const folder = await live.locate(find(live, "Workspace/lib")!);
  assert.equal(folder.path, "src/workspace/lib");
  assert.equal(folder.dir, true);
  assert.equal((await live.locate(find(live, "StarterPlayer")!)).found, false);
});

test("two windows see each other's edits", async () => {
  const { dir } = await makeProject({ "src/workspace/Lamp.Part.inst.json": '{"props":{"Name2":{"t":"String","v":"a"}}}\n' });
  const { server } = startServer(dir);
  const url = await running(server);
  const one = connect(url);
  const two = connect(url);
  const lamp = await waitForInstance(one.live, "Workspace/Lamp");
  await waitForInstance(two.live, "Workspace/Lamp");

  one.live.setProperty(lamp, "Name2", { t: "String", v: "b" });
  await waitFor("the other window to update", () => {
    const value = two.live.store.get(lamp)?.props?.Name2;
    return value?.v === "b";
  });
});

test("a connected Studio is counted, and placeIds don't apply to the extension", async () => {
  const { dir } = await makeProject({}, "  placeIds: [42]\n");
  const { server } = startServer(dir);
  const url = await running(server);
  const { live, pluginCounts, problems } = connect(url);
  await waitFor("a snapshot", () => live.store.root !== undefined);
  assert.deepEqual(problems, []);

  const studio = new WebSocket(url);
  await new Promise((resolve) => studio.addEventListener("open", resolve));
  studio.send(JSON.stringify({ type: "hello", seq: 1, data: { protocolVersion: 1, placeId: 42 } }));
  await waitFor("Studio to count", () => pluginCounts.at(-1) === 1);

  studio.close();
  await waitFor("Studio to leave", () => pluginCounts.at(-1) === 0);
});

test("a refused connection says why and doesn't keep trying", async () => {
  // An older server, or one that doesn't speak this protocol version, refuses the hello.
  let connections = 0;
  const refuser = http.createServer();
  refuser.on("upgrade", (request, socket) => {
    connections += 1;
    const accept = createHash("sha1").update(`${request.headers["sec-websocket-key"]}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`).digest("base64");
    socket.write(`HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`);
    const body = Buffer.from('{"type":"error","seq":1,"data":{"seq":1,"message":"nope"}}');
    socket.write(Buffer.concat([Buffer.from([0x81, body.length]), body]));
    setTimeout(() => socket.destroy(), 10);
  });
  await new Promise<void>((resolve) => refuser.listen(0, "127.0.0.1", resolve));
  cleanups.push(() => void refuser.close());
  const { port } = refuser.address() as AddressInfo;

  const { problems } = connect(`ws://127.0.0.1:${port}/ws`);
  await waitFor("the refusal", () => problems.length > 0);
  assert.deepEqual(problems, ["nope"]);

  await new Promise((resolve) => setTimeout(resolve, 600)); // two retries' worth
  assert.equal(connections, 1);
});

test("a failed edit resyncs from the server", async () => {
  const { dir } = await makeProject({ "src/workspace/Lamp.Part.inst.json": "{}\n" });
  fs.appendFileSync(path.join(dir, "project.yml"), 'ignore:\n  paths: ["**/Blocked*"]\n');
  const { server } = startServer(dir);
  const { live, problems } = connect(await running(server));
  const lamp = await waitForInstance(live, "Workspace/Lamp");

  // Its new file name would match ignore.paths, so the server can't save it.
  live.rename(lamp, "Blocked");
  await waitFor("the error", () => problems.length > 0);
  assert.match(problems[0], /Blocked/);
  await waitFor("a new snapshot", () => live.store.root !== undefined && find(live, "Workspace/Blocked"));
});
