// SPDX-License-Identifier: MPL-2.0

import assert from "node:assert/strict";
import { test } from "node:test";
import { formatLogLine, isNoise, parseLogLine, problemFrom, wsUrl } from "../logs";
import { buildPanel } from "../panel";
import { looksLikeVerdeConfig } from "../project";
import { Snapshot } from "../protocol";
import { findBinary } from "../server";
import { InstanceStore } from "../store";
import { applyEdit, describe } from "../values";

const SNAPSHOT: Snapshot = {
  root: "game",
  mounts: [
    { id: "game", path: [], sync: false },
    { id: "w", path: ["Workspace"], sync: true },
    { id: "sp", path: ["StarterPlayer"], sync: false },
  ],
  instances: [
    { id: "game", name: "game", class: "DataModel" },
    { id: "w", parent: "game", name: "Workspace", class: "Workspace", props: { Gravity: { t: "Number", v: 196 } } },
    { id: "sp", parent: "game", name: "StarterPlayer", class: "StarterPlayer" },
    { id: "car", parent: "w", name: "Car", class: "Model" },
    { id: "b", parent: "car", name: "wheel10", class: "Part" },
    { id: "a", parent: "car", name: "Wheel2", class: "Part" },
    {
      id: "main",
      parent: "w",
      name: "Main",
      class: "Script",
      props: {
        Source: { t: "String", v: "print('hi')" },
        Disabled: { t: "Bool", v: true },
        "$tags": { t: "Tags", v: ["One", "Two"] },
        "$attributes": { t: "Attributes", v: [["Speed", { t: "Number", v: 5 }]] },
        Target: { t: "Ref", v: "car" },
      },
    },
  ],
};

function store(): InstanceStore {
  const out = new InstanceStore();
  out.load(SNAPSHOT);
  return out;
}

test("children sort by name, ignoring case and counting numbers", () => {
  assert.deepEqual(store().children("car").map((child) => child.name), ["Wheel2", "wheel10"]);
});

test("patches apply in order, and removing takes the subtree", () => {
  const s = store();
  s.apply({
    added: [{ id: "new", parent: "car", name: "Seat", class: "Seat" }],
    updated: [{ id: "a", name: "Front", parent: "w", props: { Anchored: { t: "Bool", v: true } } }],
    removed: ["car"],
  });
  assert.equal(s.get("car"), undefined);
  assert.equal(s.get("new"), undefined);
  assert.equal(s.get("b"), undefined);
  assert.equal(s.path("a"), "Workspace/Front");
  assert.deepEqual(s.get("a")?.props, { Anchored: { t: "Bool", v: true } });
  assert.deepEqual(s.children("w").map((child) => child.name), ["Front", "Main"]);
});

test("an update only changes what it carries", () => {
  const s = store();
  s.apply({ updated: [{ id: "main", props: { Disabled: { t: "Bool", v: false } } }] });
  assert.equal(s.get("main")?.name, "Main");
  assert.deepEqual(s.get("main")?.props?.Source, { t: "String", v: "print('hi')" });
});

test("an unknown ref in a patch is ignored", () => {
  const s = store();
  s.apply({ updated: [{ id: "nope", name: "x" }], removed: ["nope"] });
  assert.equal(s.children("w").length, 2);
});

test("the panel hides a script's Source and splits out attributes and tags", () => {
  const panel = buildPanel(store(), "main")!;
  assert.deepEqual(panel.properties.map((row) => row.name), ["Disabled", "Target"]);
  assert.deepEqual(panel.tags, ["One", "Two"]);
  assert.deepEqual(panel.attributes.map((row) => [row.name, row.editor]), [["Speed", { kind: "readonly", text: "5" }]]);
  assert.deepEqual(panel.properties[1].editor, { kind: "readonly", text: "Car" });
  assert.equal(panel.path, "Workspace/Main");
});

test("a Source on anything but a script is an ordinary property", () => {
  const s = store();
  s.apply({ updated: [{ id: "a", props: { Source: { t: "String", v: "x" } } }] });
  assert.deepEqual(buildPanel(s, "a")!.properties.map((row) => row.name), ["Source"]);
});

test("a mount's properties are read-only", () => {
  const panel = buildPanel(store(), "w")!;
  assert.equal(panel.isMount, true);
  assert.deepEqual(panel.properties[0].editor, { kind: "readonly", text: "196" });
});

test("only sync mounts and instances can be opened", () => {
  assert.equal(buildPanel(store(), "w")!.canOpen, true);
  assert.equal(buildPanel(store(), "sp")!.canOpen, false);
  assert.equal(buildPanel(store(), "car")!.canOpen, true);
});

test("the panel has nothing for an unknown instance", () => {
  assert.equal(buildPanel(store(), "nope"), undefined);
});

function edited(old: Parameters<typeof applyEdit>[0], input: unknown) {
  const result = applyEdit(old, input);
  if ("error" in result) {
    throw new Error(result.error);
  }
  return result.value;
}

test("edits keep the property's type", () => {
  assert.deepEqual(edited({ t: "Bool", v: true }, false), { t: "Bool", v: false });
  assert.deepEqual(edited({ t: "String", v: "a" }, ""), { t: "String", v: "" });
  assert.deepEqual(edited({ t: "Number", v: 1 }, ["2.5"]), { t: "Number", v: 2.5 });
  assert.deepEqual(edited({ t: "BrickColor", v: 1 }, ["194"]), { t: "BrickColor", v: 194 });
  assert.deepEqual(edited({ t: "Vector3", v: [1, 2, 3] }, ["4", "5", "6"]), { t: "Vector3", v: [4, 5, 6] });
  assert.deepEqual(edited({ t: "UDim2", v: [0, 1, 0, 2] }, ["1", "0", "1", "0"]), { t: "UDim2", v: [1, 0, 1, 0] });
  assert.deepEqual(edited({ t: "Enum", v: ["Material", "Plastic"] }, "Wood"), { t: "Enum", v: ["Material", "Wood"] });
  assert.deepEqual(edited({ t: "Color3", v: [0, 0, 0] }, "#ff0000"), { t: "Color3", v: [1, 0, 0] });
});

test("an empty Content has no value", () => {
  assert.deepEqual(edited({ t: "Content", v: "rbxassetid://1" }, ""), { t: "Content" });
  assert.deepEqual(describe({ t: "Content" }), { kind: "text", value: "" });
});

test("editing a CFrame's position keeps its rotation", () => {
  const cframe = { t: "CFrame", v: [1, 2, 3, 1, 0, 0, 0, 0, 1, 0, -1, 0] };
  assert.deepEqual(describe(cframe), {
    kind: "numbers",
    fields: [
      { label: "X", value: "1" },
      { label: "Y", value: "2" },
      { label: "Z", value: "3" },
    ],
  });
  assert.deepEqual(edited(cframe, ["7", "8", "9"]).v, [7, 8, 9, 1, 0, 0, 0, 0, 1, 0, -1, 0]);
});

test("bad edits are refused", () => {
  for (const [old, input] of [
    [{ t: "Number", v: 1 }, ["abc"]],
    [{ t: "Number", v: 1 }, [""]],
    [{ t: "Number", v: 1 }, ["Infinity"]],
    [{ t: "Vector3", v: [1, 2, 3] }, ["1", "2"]],
    [{ t: "Bool", v: true }, "true"],
    [{ t: "Enum", v: ["Material", "Plastic"] }, "not an item"],
    [{ t: "Color3", v: [0, 0, 0] }, "red"],
    [{ t: "Ref", v: "x" }, "y"],
    [{ t: "Nil" }, "y"],
  ] as const) {
    assert.ok("error" in applyEdit(old, input), `${JSON.stringify(old)} accepted ${JSON.stringify(input)}`);
  }
});

test("a value that doesn't match its type is shown, not edited", () => {
  assert.equal(describe({ t: "Vector3", v: [1, 2] }).kind, "readonly");
  assert.equal(describe({ t: "Bool", v: "yes" }).kind, "readonly");
  assert.equal(describe({ t: "Nil" }).kind, "readonly");
});

test("colors clamp and round for the picker", () => {
  assert.deepEqual(describe({ t: "Color3", v: [1.5, -1, 0.5] }), { kind: "color", hex: "#ff0080", text: "255, 0, 128" });
});

test("log lines parse, and anything else is kept as text", () => {
  const log = parseLogLine('{"level":"warn","time":"t","caller":"c","msg":"rescan failed, keeping current tree","error":"bad"}');
  assert.deepEqual(log, { level: "warn", message: "rescan failed, keeping current tree", fields: { error: "bad" } });
  assert.equal(formatLogLine(log), "[warn] rescan failed, keeping current tree error=bad");
  assert.match(problemFrom(log)!, /bad$/);

  assert.deepEqual(parseLogLine("error: nope"), { level: "text", message: "error: nope", fields: {} });
  assert.equal(parseLogLine("{not json").level, "text");
});

test("routine log lines aren't problems", () => {
  assert.equal(problemFrom(parseLogLine('{"level":"warn","msg":"websocket connection","error":"x"}')), undefined);
  assert.equal(isNoise(parseLogLine('{"level":"info","msg":"request"}')), true);
  assert.equal(isNoise(parseLogLine('{"level":"warn","msg":"request"}')), false);
});

test("the address to connect to is never the wildcard", () => {
  assert.equal(wsUrl("127.0.0.1:45678"), "ws://127.0.0.1:45678/ws");
  assert.equal(wsUrl("0.0.0.0:45678"), "ws://127.0.0.1:45678/ws");
  assert.equal(wsUrl("[::]:45678"), "ws://[::1]:45678/ws");
  assert.equal(wsUrl("localhost:1"), "ws://localhost:1/ws");
});

test("only Verde's project.yml counts", () => {
  assert.equal(looksLikeVerdeConfig("version: 0\nserve:\n  port: 1\nsync:\n  - instance: W\n    path: s\n"), true);
  assert.equal(looksLikeVerdeConfig("serve: {port: 1}\nsync:\n  - {instance: W, path: s}\n"), true);
  assert.equal(looksLikeVerdeConfig("name: MyApp\ntargets:\n  sync:\n    a: 1\n"), false);
});

test("finding the executable", () => {
  const has = (...files: string[]) => (file: string) => files.includes(file);
  const env = { PATH: "/a:relative:/b", HOME: "/home/me" };

  assert.equal(findBinary("/x/verde", env, "linux", has("/x/verde")), "/x/verde");
  assert.equal(findBinary("/x/verde", env, "linux", has("/b/verde")), undefined, "a configured path is never replaced");
  assert.equal(findBinary("", env, "linux", has("/a/verde", "/b/verde")), "/a/verde");
  assert.equal(findBinary("", env, "linux", has("relative/verde")), undefined, "relative PATH entries could be the workspace");
  assert.equal(findBinary("", env, "linux", has("/home/me/go/bin/verde")), "/home/me/go/bin/verde");
  assert.equal(findBinary("", { ...env, GOPATH: "/gp:/other" }, "linux", has("/gp/bin/verde")), "/gp/bin/verde");
  assert.equal(findBinary("", env, "linux", has()), undefined);
});
