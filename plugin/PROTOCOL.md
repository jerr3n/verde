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
- [x] If a write fails, log it and send `error`. The plugin warns the user and doesn't undo
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

- [x] Write non-script instances and properties to disk (see below).
- [x] Give siblings with the same name their own files.
- [ ] Store `Ref` properties in files.
- [x] Add a `verde init` command that generates `project.yml`.
- [x] Add a `verde build` command that writes place and model files (see [Building a place](#building-a-place)).
- [ ] Add end-to-end Go tests that connect as a plugin and check the messages and files.
- [ ] Run the manual Studio checks in `README.md`.
- [ ] Test large projects. Roblox doesn't document a message size limit, so big snapshots
      and patches may need splitting.

## Decisions to make

| Decision | Suggestion |
|---|---|
| How classes map to files | Use `.server.luau` for `Script`, `.client.luau` for `LocalScript`, `.luau` for `ModuleScript`, and directories for `Folder` |
| A script with children | Use a directory containing `index.server.luau`, `index.client.luau` or `index.luau` |
| Non-script instances | Stored as `.inst.json` files (see "Instances on disk") |
| `Ref` properties | Kept in memory for now, because a `Ref` changes on every load and a saved one wouldn't point at anything. They're lost on a server restart. Later, store a permanent id in the target's file |
| Siblings with the same name | The first gets the plain file name, and the others add a tag made from their Ref, with their real name and an id inside (see "Instances on disk"). Studio allows many children named `Part`, but they can't share a path |
| Empty `placeIds` | Allow any place |
| `placeId` 0 (unpublished place) | Allow it only when `placeIds` is empty |

The plugin pairs same-named siblings by what they hold, including everything under them, because
files come back in an order Studio knows nothing about. Any left over, because they changed
while disconnected, pair by position.

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
covers matches for `ignore`, names and classes that can't be written, and failed writes. Each
patch and each scan that changes something tries again. The user gets one `error` per
instance.

## Instances on disk

Anything that isn't a script or a plain `Folder` is stored in a `.inst.json` file, with its
name and class in the file name. An instance with children is a folder with an index file,
the same way `Shop/index.luau` works:

```
src/workspace/
  Lamp.Part.inst.json         a Part with no children
  Car/
    index.Model.inst.json     makes Car a Model
    Wheel.Part.inst.json
    Drive.server.luau
    Drive.Script.inst.json    Drive's properties other than Source
  lib/
    index.Folder.inst.json    lib's tags, attributes and other properties
    Util.luau
    Util.ModuleScript.inst.json
```

The file holds the wire values from [Values](#values), one to a line and sorted by name, so
that a change is a one-line diff:

```json
{
  "props": {
    "Anchored": {"t":"Bool","v":true},
    "Size": {"t":"Vector3","v":[4,1,2]}
  }
}
```

- Only properties that differ from a new instance are stored, so `{}` is a valid file.
- A script or `Folder` only has a `.inst.json` file if it has properties to store. A script's
  `Source` is always in its `.luau` file.
- The class is in the file name so that `Lamp` the Part and `Lamp` the Model can sit side by
  side, as they can in Studio, and so that finding one doesn't mean opening every file.
- `index.Script.inst.json` doesn't make a folder a script, because only `index.server.luau`
  can do that. A script's `.inst.json` left behind by a deleted script is ignored.
- Other `.json` files, like `package.lock.json`, are skipped.
- A file that can't be parsed stops the scan, and the server keeps its current tree. A
  half-saved file therefore can't delete anything from Studio.
- Deleting a property from a file doesn't reset it in Studio, because properties missing from
  a patch are left alone. The next change to that instance from Studio writes it back.

### Siblings with the same name

Two siblings with the same name and class can't share a file name. That goes for names that
differ only by case too, since they'd be the same file on Windows and macOS. The first keeps
the plain name, and each of the others adds a tag:

```
src/workspace/
  Part.Part.inst.json                   { "id": "...", "props": { ... } }
  Part~3f9a1c2e7b04.Part.inst.json      { "id": "...", "name": "Part", "props": { ... } }
  Util.luau
  Util.ModuleScript.inst.json           { "id": "..." }
  Util~8c1d07f4a2e6.luau
  Util~8c1d07f4a2e6.ModuleScript.inst.json   { "id": "...", "name": "Util" }
```

- `id` is the instance's Ref, which a load reuses instead of making a new one. That's how a
  scan tells the two apart. Only instances with a same-named sibling have one.
- `name` is there when the file name doesn't say it. It wins over the file name, so a tagged
  file can be renamed to anything.
- The tag is a hash of the Ref, not a counter, so that deleting one sibling doesn't rename the
  others. For the same reason, the survivor keeps its tag when the plain name comes free.
- A file with an id is the same instance wherever it is. Moving or renaming it by hand moves
  or renames the instance, instead of deleting it and adding another.
- A file with no id belongs to the only instance with its name and class, so two of those is
  an error, and so is two files with the same id. Copying a file makes that error: remove the
  id from the copy.

## Building a place

`verde build -o game.rbxlx` writes the project's files as a place, without a server or Studio.
`verde build -o Car.rbxmx --instance Workspace/Car` writes one instance and everything inside it
as a model. The same files give the same bytes, so two builds can be compared.

Only the XML formats are written, not the binary ones (`.rbxl` and `.rbxm`).

- A model can't be a service or another sync target, since those already exist in every game.
- A path that fits two same-named siblings is an error, because it can't say which one you mean.
- A `Ref` to something outside a model is left out with a warning.

The wire format doesn't say everything a place file needs: a `Number` could be a float, double
or int, `Size` is saved as `size`, and an `Enum` is saved as a number. That comes from
`internal/build/reflection.json`, a trimmed copy of rbx-dom's database. Run
`go generate ./internal/build` to update it when Roblox adds classes.

- A property the database doesn't know, or one whose value doesn't fit its type, is left out
  with a warning. Everything else still builds.
- A script whose `Source` XML can't hold, like one with a `\x01` in it, stops the build. So does
  a name with such a character.
- A place only has what's in the files. `Ref` properties, service properties and Terrain aren't
  stored there yet, so they aren't in it. The builder writes a `Ref` when the tree has one, but
  loading from files can't produce one until they're stored.

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
| `status` | server, to editors | Status |
| `locate` | editors | `{ "id": "..." }` |
| `located` | server | Located |

The plugin sends `ack` for every patch it applies and ignores `ack`s it receives, so the
backend can ignore them too and doesn't need to send any.

After a snapshot has arrived, the plugin logs an `error` as a warning, shows it as a
notification and stays connected. Before that, it shows the message and disconnects.

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

The Studio plugin leaves out `role`. See [Editor clients](#editor-clients) for the other value.

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

### Editor clients

A client that isn't in a place, like the VS Code extension, says hello with
`"role": "editor"`. It gets the same `snapshot` and `patch` messages as a plugin, and its
patches are handled the same way: they're written to disk and forwarded to everyone else, but
not echoed back. It differs in three ways:

- `placeIds` don't apply to it.
- It isn't counted as a connected Studio. It's sent `status` right after its snapshot, and
  whenever a plugin connects or leaves. Plugins are never sent `status`.

  ```json
  { "plugins": 1 }
  ```

- It doesn't compare the snapshot with anything, so connecting changes nothing and it can
  reconnect on its own.

Any client can ask where an instance's files are with `locate`, since only the server knows.
Paths in the answer are `/`-separated from the project root. `file` is there for scripts,
`props` if a `.inst.json` exists, and `found` is false for an instance with no files, like a
fileless one or a mount on the way to a sync target.

```json
{ "seq": 7, "found": true, "path": "src/server/Main.server.luau", "file": "src/server/Main.server.luau" }
```

`seq` is the `locate` message's. An `error` with that `seq` means it failed.

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
	Role            string `json:"role,omitempty"` // "editor", or empty for a Studio plugin
}

type Status struct {
	Plugins int `json:"plugins"`
}

type Locate struct {
	ID Ref `json:"id"`
}

type Located struct {
	Seq   int64  `json:"seq"`
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`
	File  string `json:"file,omitempty"`
	Props string `json:"props,omitempty"`
	Dir   bool   `json:"dir,omitempty"`
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
