# Verde for VS Code

Runs the Verde server from the editor, and shows your instances the way Studio does. It also
works in VSCodium and other VS Code builds, version 1.101 or newer.

## Install

You need the `verde` executable. `make install` in the repo root puts it in `$GOPATH/bin`, where
the extension will find it. Otherwise, set `verde.path`. The executable has to be from this
version of the repo or newer, because the views use messages older servers don't have.

```sh
cd vscode
npm install
npm run package
code --install-extension verde-0.1.0.vsix   # or codium
```

## Use

Open a folder with a Verde `project.yml`, or run **Verde: Create Project** in an empty one. Click
**Verde** in the status bar, or run **Verde: Start Server**. **Copy Server Address** gets the URL
to give the Studio plugin, which still connects on its own terms: the extension never decides
who wins, and connecting from here changes nothing.

The status bar says when Studio is connected. Server output is in **Verde: Show Logs**, and
problems you need to act on, like a `.inst.json` that can't be parsed, pop up as warnings.

### Explorer

The Verde tab in the activity bar shows the tree from the server, live, including changes from
disk and from Studio.

- Click a script to open its file.
- **Open File** in the context menu opens an instance's `.inst.json`, or shows a folder.
- **Rename** (F2) and **Delete** (Del) are the same as doing it in Studio. They change the files
  and any connected Studio. Deleted files go to `.verde/trash`.

### Properties

Shows the selected instance. An edit is saved when you press Enter or leave the field, and
Escape puts the old value back. It reaches disk and Studio the same way a change from Studio
would.

| Editable | Shown only |
|---|---|
| `Bool`, `String`, `Number`, `Content`, `BrickColor` | `Ref`, `Font`, `PhysicalProperties` |
| `Vector2`, `Vector3`, `UDim`, `UDim2`, `Rect`, `NumberRange` | `NumberSequence`, `ColorSequence` |
| `Color3`, `Enum`, a `CFrame`'s position | Attributes, tags, and anything on a service |

## Limits

- **Only stored properties appear.** Verde only stores values that differ from a new instance,
  and the server has no list of what each class has. A Part with default `Anchored` won't show it,
  so it can't be turned on from here. Studio has to change it first.
- **Enums aren't checked.** The extension doesn't know each Enum's items, so a name that doesn't
  exist is saved anyway, and Studio warns and skips it.
- **There's no adding or moving instances** in the Explorer yet. Create the file, or do it in
  Studio.
- **Open editors don't follow a rename.** Renaming or deleting a script that's open leaves its tab
  on the old path, the same as any other change made outside the editor. Saving unsaved text from
  that tab recreates the old file, which becomes a new instance.
- **A server you started in a terminal isn't picked up.** Starting one here fails because the port
  is taken, so stop that one first.

## Development

| Module | Job |
|---|---|
| `extension.ts` | Commands, status bar, and tying everything together |
| `explorer.ts` | The Explorer tree |
| `properties.ts`, `media/` | The Properties view and its page |
| `panel.ts`, `values.ts` | What the panel shows for an instance, and how edits become wire values |
| `live.ts`, `store.ts` | The connection to the server, and the local copy of its tree |
| `server.ts`, `logs.ts` | Running `verde serve`, and reading its output |

Everything but `extension.ts`, `explorer.ts` and `properties.ts` is free of the VS Code API, so
`npm test` runs it directly. It builds `verde` from `../executable` and runs against that, with
real projects, files and WebSockets. Press F5 with `vscode/` open to try the rest.

The extension runs the server with `GIN_MODE=release`, which makes its logs JSON. The address
comes from its `listening` line, and the warnings come from the messages in `logs.ts`, so changing
those messages in the server means changing them there too.
