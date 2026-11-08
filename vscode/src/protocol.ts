// SPDX-License-Identifier: MPL-2.0

// The wire protocol. plugin/PROTOCOL.md is the reference.

export type Ref = string;

export interface Value {
  t: string;
  v?: unknown;
}

export type Props = Record<string, Value>;

export interface Instance {
  id: Ref;
  parent?: Ref;
  name: string;
  class: string;
  props?: Props;
}

export interface Update {
  id: Ref;
  parent?: Ref;
  name?: string;
  props?: Props;
}

export interface Patch {
  added?: Instance[];
  updated?: Update[];
  removed?: Ref[];
}

export interface Mount {
  id: Ref;
  path: string[];
  sync: boolean;
}

export interface Snapshot {
  root: Ref;
  mounts: Mount[];
  instances: Instance[];
}

export interface Located {
  seq: number;
  found: boolean;
  path?: string;
  file?: string;
  props?: string;
  dir?: boolean;
}

export const PROTOCOL_VERSION = 1;
export const SCRIPT_CLASSES = new Set(["Script", "LocalScript", "ModuleScript"]);
