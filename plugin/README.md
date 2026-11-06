# Verde Studio plugin

Connects Roblox Studio to the Verde server and keeps both sides in sync.
[PROTOCOL.md](PROTOCOL.md) is the contract the backend has to follow.

## Install

```sh
cd plugin
rojo build default.project.json -o Verde.rbxm
```

Copy `Verde.rbxm` into Studio's Plugins folder (**Plugins tab → Plugins Folder**)
and restart Studio. Delete `verde_test.lua` from there if you still have it: it
connects on every load, and Studio only allows a few connections at once.

## Try it without the backend

```sh
lune run test/mock_server.luau
```

This serves a small fixed project and prints everything the plugin sends. In
Studio, open the **Verde** panel and connect to `ws://127.0.0.1:34873/ws`. You
should get `ReplicatedStorage/lib/Hello` and `ServerScriptService/Main`.

To exercise the rest:

- **Studio → server:** edit a script, rename it, add a Part or delete something.
  Each change prints as a `patch`.
- **Server → Studio:** type `source print("hi")`, `rename Other`, `add Thing` or
  `remove` into the mock server. Studio updates, with no patch echoed back.
- **Choosing a side:** start it with `VERDE_ON_CONNECT=interactive` or `studio`.
- **Notifications:** `error <text>` reports a server error, `burst 5` sends quick
  changes that merge into one pop-up, `bad` sends a patch that fails to apply, and
  `quit` stops the server so the connection drops.

## How it works

| Module | Job |
|---|---|
| `init.server.luau` | Entry point. Exits during Play, then runs the main loop |
| `Ui` | The panel's Sync, Activity and Settings tabs |
| `Ui/Toasts` | Pop-up notifications over the 3D view |
| `Ui/Kit`, `Ui/Theme` | Shared building blocks, and colors that follow Studio's theme |
| `Notifications` | The activity history and unread count, with no UI of its own |
| `Session` | Connection lifecycle and message handling |
| `Connection` | The WebSocket |
| `Sync` | Compares and applies: the core of the plugin |
| `Properties` | Reads and writes an instance's properties as wire values |
| `Values` | Converts Roblox values to and from JSON |

### The WebSocket

The user presses Connect. The plugin never connects on its own, and it doesn't
reconnect automatically, because reconnecting can overwrite Studio with what's on
disk. It sends `hello`, gets a `snapshot`, and then both sides exchange patches.

The panel remembers the server address. Press **Enter** in the address field or
click **Connect** to connect; **Cancel** stops a pending connection. When a sync
needs a decision, the panel shows the difference counts and explains each choice,
including whether keeping Studio will delete files on disk.

Incoming messages go into a queue, and one loop handles them every 0.1 seconds.
Applying a change can yield, so a single loop keeps patches from interleaving.

### Notifications

`Session` reports what happens as notices, and `Ui` decides how to show them. Every
notice goes in the **Activity** tab, which keeps the last 100. Pop-ups appear in the
corner of the 3D view, even with the panel closed.

| What happened | Pop-up | Lasts |
|---|---|---|
| Connected, disconnected | Yes | A few seconds |
| Couldn't reach the server, connection lost, connection refused | Yes, with **Reconnect** | Until dismissed |
| Disk and Studio need a decision | Yes, with **Review**. Also opens the panel | Until answered |
| No snapshot after 5 seconds | Yes | Until it arrives |
| Server error, a change that couldn't be applied or sent | Yes | 12 seconds |
| Missing sync folder, instances only in Studio | Yes | 8 seconds |
| A change from disk | Yes, can be turned off in Settings | 3 seconds |
| A change sent to disk | No, this would pop up on every pause in typing | |

Changes within 5 seconds of each other merge into one notification with running
counts. Hovering over a pop-up holds it open, and clicking it opens the panel.
**Reconnect** only runs when clicked, so the plugin still never reconnects on its own.

Studio only draws pop-ups over the 3D view, so they can't be seen from a script tab.
For that case, the panel's title shows the number of unread problems, as in
`Verde (2)`. Warnings still go to Output as well.

### The logo

The logo is uploaded as a Decal, and `rbxassetid://` needs an Image ID, so `Kit.LOGO`
uses `rbxthumb://` with the Decal's ID. To find the Image ID behind a Decal, run this in
the command bar:

```lua
print(game:GetService("InsertService"):LoadAsset(107008526393062):FindFirstChildWhichIsA("Decal", true).Texture)
```

### Connecting to a project

The plugin matches the snapshot against Studio by parent, name and class. Matching
is what stops every connect from creating duplicates, and it's why Refs don't have
to survive a server restart. `onConnect` then decides who wins. PROTOCOL.md has the
table. Connecting never removes files without asking. When a change in Studio does
remove one, the server moves it to `.verde/trash` instead of deleting it.

### Detecting changes

Events never decide what changed. They only mark an instance dirty. Each tick, the
plugin compares dirty instances against a **shadow**, a record of the last state
both sides agree on, and sends the difference.

This one rule covers the cases that usually need special handling:

- **Echoes:** applying a server change updates the shadow, so its events find no
  difference. There's no "ignore the next event" flag to get wrong.
- **Event timing:** it doesn't matter whether Studio's signals are deferred.
- **Moves:** a move is just a different parent, however the events arrive.
- **Undo and redo:** just more differences. Applied changes are recorded, so a
  person can undo what the server did.
- **Rounding:** after writing a value, the shadow stores what the engine read back.

### Scripts

Scripts are instances with one special property, `Source`.

- Reading uses the editor's text when the script is open, since that can be newer
  than `Source`.
- Writing goes through `UpdateSourceAsync` when the script is open, so the editor
  doesn't keep showing old text.
- While someone types, sending waits for a 0.5 second pause.

### Other instances

Any class works. The plugin asks `ReflectionService` which properties each class
saves with the place, so there's no property list to maintain.

- New instances send only the properties that differ from a fresh instance of that
  class, plus `Source`. A Part sends a few values rather than dozens.
- Properties missing from an incoming instance are left alone.
- Properties pointing at other instances are sent as Refs.
- `$attributes` and `$tags` are synced like properties.
- Never synced: `Camera`, `Terrain`, anything with `Archivable = false`, and
  whatever the config's `ignore` lists.
- Properties of the mounted services themselves, such as `Lighting.Brightness`,
  aren't synced. Only what's inside them is.

## Testing

```sh
lune run test/run.luau
lune run test/ui.luau
```

The first suite runs the real sync modules against a fake Studio
(`test/fake_studio.luau`), using real Roblox data types and deferred events. The UI
suite checks the panel, activity feed, pop-ups and settings against real Roblox
instance property types with fake plugin events and tweens. `init.server.luau` isn't
covered.

Lune doesn't render UI. In Studio, also check:

- The panel in light and dark themes, resized to a narrow, short dock. Long errors
  should wrap, and the full sync review should scroll into view.
- The logo shows in the panel header and on the toolbar button.
- Pop-ups slide in at the bottom right of the 3D view, and their buttons work.

It can't catch wrong assumptions about Studio itself. These still have to be
checked in real Studio:

1. **Property discovery.** Paste this in the command bar. It should list things like
   `Anchored`, `Size` and `CFrame`, but not `Position`. If it prints nothing, only
   scripts will sync.
   ```lua
   for _, p in game:GetService("ReflectionService"):GetPropertiesOfClass("Part") do
       if p.Serialized and p.Display and not p.Display.DeprecationMessage then print(p.Name) end
   end
   ```
2. **Typing in an open script** sends a patch after a pause, and a server change
   shows up in the open editor.
3. **Deleting a folder with children, then pressing undo** sends a removal, then an
   addition.
4. **Moving an instance between folders** sends one update, not a removal and an
   addition.
5. **Undoing a server change** works and sends the old value back.
6. **Large projects:** Roblox doesn't document a message size limit.

## Known limits

- **Tags:** Roblox has no event for tag changes, so a tag change is only sent the next
  time something else on that instance changes.
- **Things a plugin may not be able to write,** such as `MeshPart.MeshId`, log a warning
  and are skipped. Non-script binary data like Terrain voxels isn't handled.
- **Properties that point at other instances** aren't stored in files yet, so they're lost
  on a server restart. Studio keeps its own values, but a place rebuilt from files won't
  have them, such as `Model.PrimaryPart` or `ObjectValue.Value`.
- **Same-named siblings that changed while disconnected** pair by position, since what they
  hold no longer matches. The result on disk or in Studio is still right, but the review may
  show a change to the wrong one of them.
- **Crossing edits:** see PROTOCOL.md.
