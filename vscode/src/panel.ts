// SPDX-License-Identifier: MPL-2.0

import { Ref, Value } from "./protocol";
import { InstanceStore } from "./store";
import { describe, Editor } from "./values";

export interface PropertyRow {
  name: string;
  type: string;
  editor: Editor;
}

// What the Properties panel draws for one instance.
export interface PanelState {
  id: Ref;
  name: string;
  class: string;
  path: string;
  isMount: boolean;
  canOpen: boolean;
  properties: PropertyRow[];
  attributes: PropertyRow[];
  tags: string[];
}

export function buildPanel(store: InstanceStore, id: Ref): PanelState | undefined {
  const instance = store.get(id);
  if (!instance) {
    return undefined;
  }
  const refName = (ref: Ref) => store.get(ref)?.name;

  const isScript = store.isScript(id);
  const properties: PropertyRow[] = [];
  let attributes: PropertyRow[] = [];
  let tags: string[] = [];
  for (const [name, value] of Object.entries(instance.props ?? {})) {
    if (name === "$attributes" && value.t === "Attributes") {
      attributes = readAttributes(value, refName);
    } else if (name === "$tags" && value.t === "Tags" && Array.isArray(value.v)) {
      tags = value.v.filter((tag): tag is string => typeof tag === "string");
    } else if (!(isScript && name === "Source")) {
      properties.push({
        name,
        type: value.t,
        // Mounts' properties aren't synced, so an edit to one would go nowhere.
        editor: store.isMount(id) ? { kind: "readonly", text: textOf(value, refName) } : describe(value, refName),
      });
    }
  }
  properties.sort((a, b) => a.name.localeCompare(b.name));

  return {
    id,
    name: instance.name,
    class: instance.class,
    path: store.path(id),
    isMount: store.isMount(id),
    canOpen: !store.isMount(id) || store.isSyncMount(id),
    properties,
    attributes,
    tags,
  };
}

function readAttributes(value: Value, refName: (ref: Ref) => string | undefined): PropertyRow[] {
  if (!Array.isArray(value.v)) {
    return [];
  }
  const rows: PropertyRow[] = [];
  for (const entry of value.v) {
    if (Array.isArray(entry) && typeof entry[0] === "string" && entry[1] && typeof entry[1].t === "string") {
      rows.push({
        name: entry[0],
        type: entry[1].t,
        editor: { kind: "readonly", text: textOf(entry[1], refName) },
      });
    }
  }
  return rows;
}

function textOf(value: Value, refName: (ref: Ref) => string | undefined): string {
  const editor = describe(value, refName);
  switch (editor.kind) {
    case "bool":
      return String(editor.value);
    case "text":
      return editor.value;
    case "numbers":
      return editor.fields.map((field) => field.value).join(", ");
    case "color":
      return editor.text;
    case "readonly":
      return editor.text;
  }
}
