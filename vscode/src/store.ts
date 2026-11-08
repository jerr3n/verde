// SPDX-License-Identifier: MPL-2.0

import { Instance, Mount, Patch, Ref, SCRIPT_CLASSES, Snapshot } from "./protocol";

// The extension's copy of the server's tree, built from a snapshot and kept current by patches.
export class InstanceStore {
  private instances = new Map<Ref, Instance>();
  private childIds = new Map<Ref, Set<Ref>>();
  private mounts = new Map<Ref, Mount>();
  private rootId: Ref | undefined;

  get root(): Ref | undefined {
    return this.rootId;
  }

  clear(): void {
    this.instances.clear();
    this.childIds.clear();
    this.mounts.clear();
    this.rootId = undefined;
  }

  load(snapshot: Snapshot): void {
    this.clear();
    this.rootId = snapshot.root;
    for (const mount of snapshot.mounts) {
      this.mounts.set(mount.id, mount);
    }
    for (const instance of snapshot.instances) {
      this.add(instance);
    }
  }

  // Same order as the server: added, updated, then removed.
  apply(patch: Patch): void {
    for (const instance of patch.added ?? []) {
      this.add(instance);
    }
    for (const update of patch.updated ?? []) {
      const instance = this.instances.get(update.id);
      if (!instance) {
        continue;
      }
      if (update.parent && update.parent !== instance.parent) {
        this.unlink(instance);
        instance.parent = update.parent;
        this.link(instance);
      }
      if (update.name) {
        instance.name = update.name;
      }
      if (update.props) {
        instance.props = { ...instance.props, ...update.props };
      }
    }
    for (const id of patch.removed ?? []) {
      this.remove(id);
    }
  }

  get(id: Ref): Instance | undefined {
    return this.instances.get(id);
  }

  // Sorted by name, like Studio's Explorer.
  children(id: Ref): Instance[] {
    const out: Instance[] = [];
    for (const child of this.childIds.get(id) ?? []) {
      const instance = this.instances.get(child);
      if (instance) {
        out.push(instance);
      }
    }
    return out.sort((a, b) => compareNames(a.name, b.name) || compareNames(a.class, b.class));
  }

  // Ref's path from under the DataModel, like "Workspace/Car/Wheel".
  path(id: Ref): string {
    const names: string[] = [];
    for (let at = this.instances.get(id); at && at.parent; at = this.instances.get(at.parent)) {
      names.unshift(at.name);
    }
    return names.join("/");
  }

  // Mounts exist in the game already, so they can't be renamed or deleted, and their
  // properties aren't synced.
  isMount(id: Ref): boolean {
    return this.mounts.has(id);
  }

  isSyncMount(id: Ref): boolean {
    return this.mounts.get(id)?.sync === true;
  }

  isScript(id: Ref): boolean {
    const instance = this.instances.get(id);
    return instance !== undefined && SCRIPT_CLASSES.has(instance.class);
  }

  private add(instance: Instance): void {
    const copy = { ...instance };
    this.instances.set(copy.id, copy);
    this.link(copy);
  }

  private remove(id: Ref): void {
    const instance = this.instances.get(id);
    if (!instance) {
      return;
    }
    for (const child of [...(this.childIds.get(id) ?? [])]) {
      this.remove(child);
    }
    this.unlink(instance);
    this.instances.delete(id);
    this.childIds.delete(id);
  }

  private link(instance: Instance): void {
    if (!instance.parent) {
      return;
    }
    let siblings = this.childIds.get(instance.parent);
    if (!siblings) {
      siblings = new Set();
      this.childIds.set(instance.parent, siblings);
    }
    siblings.add(instance.id);
  }

  private unlink(instance: Instance): void {
    if (instance.parent) {
      this.childIds.get(instance.parent)?.delete(instance.id);
    }
  }
}

function compareNames(a: string, b: string): number {
  return a.localeCompare(b, undefined, { sensitivity: "base", numeric: true });
}
