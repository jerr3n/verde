// SPDX-License-Identifier: MPL-2.0

import * as vscode from "vscode";
import { Instance, Ref, SCRIPT_CLASSES } from "./protocol";
import { InstanceStore } from "./store";

const REFRESH_DELAY_MS = 30;

// Shows the tree the way Studio's Explorer does, without the DataModel itself.
export class ExplorerProvider implements vscode.TreeDataProvider<Ref> {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.changed.event;
  private refreshTimer: NodeJS.Timeout | undefined;

  constructor(private readonly store: InstanceStore) {}

  // One save or `git checkout` can be many patches, so these are grouped.
  refresh(): void {
    clearTimeout(this.refreshTimer);
    this.refreshTimer = setTimeout(() => this.changed.fire(), REFRESH_DELAY_MS);
  }

  getChildren(id?: Ref): Ref[] {
    const parent = id ?? this.store.root;
    return parent === undefined ? [] : this.store.children(parent).map((child) => child.id);
  }

  getTreeItem(id: Ref): vscode.TreeItem {
    const instance = this.store.get(id);
    if (!instance) {
      return new vscode.TreeItem("");
    }
    const hasChildren = this.store.children(id).length > 0;
    const item = new vscode.TreeItem(
      instance.name,
      hasChildren ? vscode.TreeItemCollapsibleState.Collapsed : vscode.TreeItemCollapsibleState.None,
    );
    item.id = id;
    item.description = instance.class === instance.name ? undefined : instance.class;
    item.tooltip = `${this.store.path(id)}\n${instance.class}`;
    item.iconPath = iconFor(instance, this.store.isMount(id));
    item.contextValue = this.store.isSyncMount(id) ? "syncMount" : this.store.isMount(id) ? "mount" : "instance";
    if (SCRIPT_CLASSES.has(instance.class)) {
      item.command = { command: "verde.openInstance", title: "Open File", arguments: [id] };
    }
    return item;
  }
}

const ICONS: Record<string, string> = {
  Folder: "folder",
  Script: "file-code",
  LocalScript: "file-code",
  ModuleScript: "symbol-module",
  Model: "package",
  Part: "primitive-square",
  MeshPart: "primitive-square",
  WedgePart: "primitive-square",
  TrussPart: "primitive-square",
  SpawnLocation: "location",
  Sound: "unmute",
  SoundGroup: "unmute",
  ScreenGui: "window",
  SurfaceGui: "window",
  BillboardGui: "window",
  Frame: "layout",
  ScrollingFrame: "layout",
  TextLabel: "symbol-text",
  TextButton: "symbol-text",
  TextBox: "symbol-text",
  ImageLabel: "file-media",
  ImageButton: "file-media",
  Decal: "file-media",
  Texture: "file-media",
  PointLight: "lightbulb",
  SpotLight: "lightbulb",
  SurfaceLight: "lightbulb",
  RemoteEvent: "radio-tower",
  RemoteFunction: "radio-tower",
  BindableEvent: "zap",
  BindableFunction: "zap",
};

function iconFor(instance: Instance, isMount: boolean): vscode.ThemeIcon {
  if (isMount) {
    return new vscode.ThemeIcon("server-environment");
  }
  if (instance.class.endsWith("Value")) {
    return new vscode.ThemeIcon("symbol-variable");
  }
  return new vscode.ThemeIcon(ICONS[instance.class] ?? "symbol-class");
}
