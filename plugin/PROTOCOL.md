# Verde protocol and backend to-do

This file has two jobs:

- **[Backend to-do](#backend-to-do):** everything left to build in `executable/`, in order.
- **[Wire protocol](#wire-protocol):** the contract the Studio plugin expects from the backend.

Everything goes over one WebSocket, as JSON text messages. The plugin connects to
whatever URL the user types in, such as `ws://127.0.0.1:45678/ws`.

The plugin decides who wins on connect, so the backend doesn't resolve conflicts. It
sends a snapshot, applies patches, and keeps disk in step.

## Backend to-do

**Already done:** the `verde serve` command, logging, config loading, and a `/ws` route
that only echoes messages.

### 1. Fix the module

- [x] Run `go mod tidy`.
- [ ] Decide whether to stay on `go.yaml.in/yaml/v4`, which is a release candidate
      (`v4.0.0-rc.9`).

### 2. Finish `internal/config`

- [x] `config.Load(path)` parses strictly, applies defaults and validates. Unknown fields,
      unsupported versions, invalid ports and `onConnect` values, and overlapping or
      escaping `sync` entries are errors.
- [x] `address` defaults to `127.0.0.1`, and `onConnect` defaults to `disk`.
- [x] `Cfg.Root` holds the config file's directory. Join it with each cleaned `sync` path
      to get the real location, instead of using the working directory.
- [x] Tests in `internal/config/config_test.go`.
- [x] Check that `ignore.paths` patterns are valid. `project.Load` does this with
      `doublestar.ValidatePattern`.
- [x] Stop symlinks leading outside the project when reading. `project.Load` uses
      `os.OpenRoot` and doesn't follow symlinks inside sync folders.
- [x] Use `os.OpenRoot` for the file changes in step 8 as well.

### 3. Use the config in `serve`

- [x] Load `project.yml` at startup and listen on `Cfg.Addr()`.
- [x] Remove the hardcoded `:8080` and the `PORT` lookup.
- [ ] Replace `log.Printf` in the WebSocket code with the zap logger.

### 4. Add `internal/protocol`

- [x] Add the types from [Go types](#go-types).

### 5. Add `internal/tree`

- [x] Write the tree as a package using the protocol types: a map from Ref to instance,
      with insert, update, remove and a parents-first list.
- [x] Make removing an unknown Ref a no-op.
- [x] Accept Refs made by the plugin.
- [x] Store a `Nil` property as `Nil` instead of deleting it. A property missing from a
      snapshot is left alone in Studio, so deleting it would hide the change on reconnect.
- [x] Add tests for moves, cycles, subtree removal and repeated removal.

### 6. Add `internal/project` (disk → tree)

- [x] Build the mounts. Add the DataModel with `sync: false`, each parent on the way to
      a target with `sync: false`, and each `sync` target with `sync: true`. For
      `StarterPlayer/StarterPlayerScripts`, that means `StarterPlayer` is `false` and
      `StarterPlayerScripts` is `true`.
- [x] Generate a new Ref for each instance read from disk, using the standard library's
      `uuid` package.
- [x] Read each `sync` path into instances. Skip whatever matches `ignore.paths`, using
      `doublestar`.
- [x] Filter `ignore.instances` and `ignore.classes` out of the snapshot. The plugin only
      applies them to what's in Studio, so it would still create an ignored instance
      that the snapshot contains.

### 7. Add `internal/session` and the handshake

- [ ] Always use non-nil slices for `Mount.Path`, `Snapshot.Mounts` and
      `Snapshot.Instances`. A nil slice encodes as `null`, and the plugin rejects a
      snapshot whose `mounts` or `instances` isn't a list.

- [ ] Let one goroutine own the tree. Connections and the file watcher send it events
      over channels. This avoids locks and gives the server one order for changes.
- [ ] Give each connection a read loop, its own outgoing queue, and a writer goroutine,
      so a slow client can't hold up the others.
- [ ] Call `conn.SetReadLimit(...)` with something generous. `coder/websocket` closes
      connections on messages over 32 KiB by default, and one large script exceeds that.
- [ ] Number each connection's outgoing messages from 1.
- [ ] Handle `hello`. Reply with `error` for the wrong `protocolVersion` or a place not
      allowed by `placeIds`, and with `snapshot` otherwise.
- [ ] Build the snapshot and register the connection in one step. Otherwise, a change
      made between the two is lost for that client.
- [ ] Reject patches sent before `hello` with `error`.

**Milestone:** connect from Studio and see your scripts appear.

### 8. Handle Studio → disk

For each `patch` from the plugin:

- [x] Apply `added`, then `updated`, then `removed`.
- [x] Accept any class and any property. Scripts are only special because `Source`
      reaches a file.
- [x] Keep everything in the tree, even what can't reach disk yet.
- [x] Update the tree, then write the file. Write to a temporary file and rename it, so a
      crash can't leave half a script.
- [x] Handle renames and moves, including every path under a moved folder.
- [x] Handle `removed` by moving the files for that subtree to `.verde/trash`.
- [x] Never build a path directly from an instance name. Names are percent-encoded (see
      [Names on disk](#names-on-disk)).
- [x] Forward the patch to every other connected plugin, but not to the sender.
- [x] If a write fails, log it and send `error`. The plugin only warns and doesn't undo
      anything, so the two sides now differ until the user reconnects.

**Milestone:** edit, rename, add and delete in Studio, and see the files change.

### 9. Handle disk → Studio

- [ ] Watch each `sync` path with `fsnotify`, which is a new dependency.
- [ ] Watch directories rather than files, since many editors save by replacing the file.
- [ ] Add a watch for each new directory, because `fsnotify` isn't recursive.
- [ ] Debounce events, so one save or one `git checkout` becomes one patch.
- [ ] Ignore `ignore.paths` and your own temporary files.
- [ ] Re-read what changed, compare it with the tree, and send only real differences.
      This also stops your own writes from echoing back, so there's no need for an
      "ignore the next event" flag.
- [ ] Send the patch to all connected plugins.

A file rename can arrive as a removal and a creation, which replaces the instance in
Studio and gives it a new Ref. That's fine at first. Detecting real renames can come later.

**Milestone:** edit, rename, add and delete files, and see Studio change.

### 10. Finish

- [ ] Write non-script instances and properties to disk (see below).
- [x] Add a `verde init` command that generates `project.yml`.
- [ ] Add end-to-end Go tests that connect as a plugin and check the messages and files.
- [ ] Run the manual Studio checks in `README.md`.
- [ ] Test large projects. Roblox doesn't document a message size limit, so big snapshots
      and patches may need splitting.

## Decisions to make

| Decision | Suggestion |
|---|---|
| How classes map to files | Use `.server.luau` for `Script`, `.client.luau` for `LocalScript`, `.luau` for `ModuleScript`, and directories for `Folder` |
| A script with children | Use a directory containing `index.server.luau`, `index.client.luau` or `index.luau` |
| Non-script instances | Kept in memory for now, along with any scripts inside them. Later, store them as JSON files. Until then, they stop syncing after a server restart |
| Siblings with the same name | The first gets the file, and the others stay in memory with a warning until the name is free. Later, add a stable suffix to the file name and store the real name inside. Studio allows many children named `Part`, but they can't share a path |
| Empty `placeIds` | Allow any place |
| `placeId` 0 (unpublished place) | Allow it only when `placeIds` is empty |

The plugin matches same-named siblings by name and class, in the order they're listed.
Keep that order stable between connects, or their properties can get swapped.

## Names on disk

Instance names can hold anything, so `project.Apply` percent-encodes what isn't safe in a
file name and scans decode it. These all come back as the same name:

| Encoded | Why |
|---|---|
| `% / \ : * ? " < > \|` and control characters | Not allowed in file names on some systems, or would change the path |
| A leading `.` | Would hide the file, and could make `.git` |
| A trailing `.` or space | Windows drops them |
| A trailing `.server` or `.client` | A ModuleScript called `x.server` would read back as a Script called `x` |
| `index` | `index.luau` means "the script of this folder" |
| `CON`, `NUL`, `COM1` and the like | Reserved on Windows |

An instance that should have a file but can't is marked *fileless*: it stays in the tree
and the snapshot, and scans leave it alone. Otherwise, a scan would take the missing file
as a deletion and remove it from Studio, or send an older file's text over an edit. This
covers siblings whose names match (ignoring case, for Windows and macOS), matches for
`ignore`, a script whose folder name is taken, and failed writes. Each patch and each scan
that changes something tries again. The user gets one `error` per instance.

## Wire protocol

### Envelope

Every message in both directions:

```json
{ "type": "patch", "seq": 12, "data": { } }
```

Each side numbers its own messages from 1. The plugin uses `seq` only to report which
message an `ack` or `error` refers to.

| `type` | Sent by | `data` |
|---|---|---|
| `hello` | plugin | Hello |
| `snapshot` | server | Snapshot |
| `patch` | both | Patch |
| `ack` | both | `{ "seq": 12 }`: that message was applied |
| `error` | both | `{ "seq": 12, "message": "..." }` |

The plugin sends `ack` for every patch it applies and ignores `ack`s it receives, so the
backend can ignore them too and doesn't need to send any.

After a snapshot has arrived, the plugin only logs an `error` as a warning and stays
connected. Before that, it shows the message and disconnects.

### Connecting

```text
plugin → hello
server → snapshot
plugin    compares the snapshot with Studio (see "How the snapshot is applied")
plugin → patch      only if Studio's side won
both   → patch ...
```

The plugin never connects or reconnects on its own. If no snapshot arrives within 5
seconds, it tells the user but keeps waiting.

Patches sent while the plugin is asking the user to choose a side wait in a queue and are
applied afterwards, so the backend doesn't need to hold them back.

### Hello

```json
{ "protocolVersion": 1, "client": "verde-plugin/0.1.0", "placeId": 1234567890 }
```

`placeId` is 0 for an unpublished place. To refuse a connection, reply with `error`.

### Snapshot

```json
{
  "root": "ref-game",
  "onConnect": "disk",
  "ignore": { "instances": ["Workspace/Camera"], "classes": ["Terrain"] },
  "mounts": [
    { "id": "ref-game", "path": [], "sync": false },
    { "id": "ref-sp", "path": ["StarterPlayer"], "sync": false },
    { "id": "ref-sps", "path": ["StarterPlayer", "StarterPlayerScripts"], "sync": true }
  ],
  "instances": [
    { "id": "ref-game", "name": "game", "class": "DataModel" },
    { "id": "ref-sp", "parent": "ref-game", "name": "StarterPlayer", "class": "StarterPlayer" },
    { "id": "ref-sps", "parent": "ref-sp", "name": "StarterPlayerScripts", "class": "StarterPlayerScripts" },
    {
      "id": "ref-input",
      "parent": "ref-sps",
      "name": "Input",
      "class": "LocalScript",
      "props": { "Source": { "t": "String", "v": "print('hi')\n" } }
    }
  ]
}
```

- **`instances`** must list parents before children.
- **`mounts`** are instances that already exist in the game. The plugin finds them by path
  and never creates them. List the DataModel, every sync target, and everything on the way
  to a sync target.
- **`sync`** marks real sync targets. The plugin only watches and changes things inside
  these. `sync: false` entries are just on the way there.
- **Whole-game mapping:** the DataModel stays `sync: false`, and each service directory is
  its own `sync: true` mount.
- **`onConnect`** is `disk`, `studio` or `interactive`. Missing means `disk`.
- **`ignore`** comes from the config. `instances` are `/`-separated paths without `game/`.
  The plugin also always skips `Camera`, `Terrain`, and anything with `Archivable = false`
  when reading Studio. `ignore.paths` stays on the server.
- **`root`** isn't read by the plugin today. Send it anyway.

Refs don't need to survive a server restart. The plugin rebinds on every connect by
matching parent, name and class.

### Patch

The same shape in both directions. Receivers apply `added`, then `updated`, then
`removed`. Any field may be missing.

```json
{
  "added": [
    { "id": "...", "parent": "...", "name": "Lamp", "class": "Part", "props": { } }
  ],
  "updated": [
    { "id": "...", "name": "NewName", "parent": "...", "props": { "Anchored": { "t": "Bool", "v": true } } }
  ],
  "removed": ["...", "..."]
}
```

- **`added`** lists parents before children.
- **`updated`** carries only what changed. A move is an update with `parent`. Leave out
  `name` and `parent` when they haven't changed. The plugin would try to apply an empty
  string.
- **`removed`** removes the whole subtree of each Ref. The plugin lists descendants as well
  as their parents, in no particular order.

Patches from the plugin can contain Refs it made up: new Studio instances get a lowercase
GUID. Its `added` entries carry every non-default property.

The plugin only sends `removed`, or changes a script, when the user's change or
`onConnect` setting says it should.

### Values

Every property is `{ "t": type, "v": value }`. `Source` must be `String`.

| `t` | `v` |
|---|---|
| `String` | `"text"` |
| `Bool` | `true` |
| `Number` | `1.5` |
| `Nil` | no `v`; the property is empty |
| `Ref` | the Ref of another synced instance |
| `Enum` | `["Material", "Plastic"]` |
| `Vector2` | `[x, y]` |
| `Vector3` | `[x, y, z]` |
| `CFrame` | `[x, y, z, r00, r01, r02, r10, r11, r12, r20, r21, r22]` |
| `Color3` | `[r, g, b]`, each 0–1 |
| `BrickColor` | the color's number |
| `UDim` | `[scale, offset]` |
| `UDim2` | `[xScale, xOffset, yScale, yOffset]` |
| `Rect` | `[minX, minY, maxX, maxY]` |
| `NumberRange` | `[min, max]` |
| `NumberSequence` | `[[time, value, envelope], ...]` |
| `ColorSequence` | `[[time, r, g, b], ...]` |
| `Font` | `[family, weightName, styleName]` |
| `PhysicalProperties` | `[density, friction, elasticity, frictionWeight, elasticityWeight]` |
| `Content` | `"rbxassetid://..."`, or no `v` for empty |

Two property names are reserved for things that aren't real properties:

| Name | `t` | `v` |
|---|---|---|
| `$attributes` | `Attributes` | `[["Name", { "t": "Number", "v": 1 }], ...]`, sorted by name |
| `$tags` | `Tags` | `["TagA", "TagB"]`, sorted |

A property with a type not listed here is skipped, not sent. That includes `Ref`s pointing
outside the synced instances.

The backend can store `v` without decoding it, so keep it as `json.RawMessage`.

### How the snapshot is applied

The plugin walks each `sync: true` mount and matches snapshot children to Studio children
by name and class. That gives three groups:

| Group | `disk` wins | `studio` wins |
|---|---|---|
| Matched, but a property in the snapshot differs | Studio takes the snapshot's value | plugin sends `updated` |
| Only in the snapshot | created in Studio | plugin sends `removed` |
| Only in Studio | **left alone and not synced**; the plugin warns | plugin sends `added` |

Properties missing from the snapshot are left as they are, so a snapshot holding only
`Source` won't reset a script's other properties.

The plugin asks the user first when `onConnect` is `interactive` and the sides differ, and
also when `studio` would delete anything on disk.

If a later server `added` has the same parent, name and class as an unsynced Studio
instance, the plugin adopts that instance instead of creating a duplicate.

## Go types

These compile and encode to the shapes above.

```go
package protocol

import "encoding/json"

type Ref string

type Envelope struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data,omitempty"`
}

type Hello struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Client          string `json:"client,omitempty"`
	PlaceID         int64  `json:"placeId"`
}

type Snapshot struct {
	Root      Ref        `json:"root"`
	OnConnect string     `json:"onConnect"`
	Ignore    Ignore     `json:"ignore"`
	Mounts    []Mount    `json:"mounts"`
	Instances []Instance `json:"instances"`
}

type Ignore struct {
	Instances []string `json:"instances"`
	Classes   []string `json:"classes"`
}

type Mount struct {
	ID   Ref      `json:"id"`
	Path []string `json:"path"` // must encode as [], not null
	Sync bool     `json:"sync"`
}

type Value struct {
	Type  string          `json:"t"`
	Value json.RawMessage `json:"v,omitempty"` // Nil has no v
}

type Instance struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name"`
	Class  string           `json:"class"`
	Props  map[string]Value `json:"props,omitempty"`
}

// Update is separate from Instance because every field but ID is optional.
type Update struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name,omitempty"`
	Props  map[string]Value `json:"props,omitempty"`
}

type Patch struct {
	Added   []Instance `json:"added,omitempty"`
	Updated []Update   `json:"updated,omitempty"`
	Removed []Ref      `json:"removed,omitempty"`
}

type Ack struct {
	Seq int64 `json:"seq"`
}

type Error struct {
	Seq     int64  `json:"seq,omitempty"`
	Message string `json:"message"`
}
```

## Known gaps

- **Crossing edits:** if both sides change the same field at the same moment, each applies
  the other's value and they end up swapped until the next reconnect. The backend's single
  goroutine gives one order for changes, but fixing this also needs a way for the plugin
  to recognise that order. The protocol doesn't have one yet.
- **Message size:** very large snapshots and patches are untested.
- **Empty names:** `updated` can't express a rename to an empty name, since a missing
  `name` and an empty one look the same. The server keeps the old name.
- **The trash is never emptied:** `.verde/trash` keeps growing until someone deletes it.
