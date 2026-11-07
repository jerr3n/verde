# Migrating from Rojo

`migrate-rojo.luau` converts a Rojo **place** project into a new Verde project.
Install [Lune](https://lune-org.github.io/docs/getting-started/1-installation/)
(version 0.10.4 or newer), keep Rojo on your PATH, and run this from the Verde
checkout:

```sh
lune run scripts/migrate-rojo.luau /path/to/game/default.project.json
```

This creates `/path/to/game/verde/`. Open that folder and run:

```sh
verde serve
```

Read `migration-report.txt`, then connect the Verde Studio plugin to
`ws://127.0.0.1:45678/ws`. The generated config uses `onConnect: interactive` so
you can review the first sync. The plugin install instructions are in
[plugin/README.md](../plugin/README.md).

The source project and existing destination folders are never overwritten. To
preview the conversion or choose another destination:

```sh
lune run scripts/migrate-rojo.luau /path/to/game --dry-run
lune run scripts/migrate-rojo.luau /path/to/game -o /path/to/game-verde
```

The destination's parent directory must already exist. If you invoke the script
from outside the Verde checkout, supply the script's full path. Keep it in this
checkout: it uses Verde's property encoder and reflection database. `--rojo PATH`
selects a different Rojo executable, and `--help` lists all options.

## What is converted

Rojo builds the project first, so its `$path` mappings, nested projects, metadata,
ignore patterns, sync rules, and supported source/model formats are resolved by
Rojo itself. Both `.project.json` and `.project.jsonc` inputs work. Omitting the
project argument looks for `default.project.json` or `default.project.jsonc` in
the working directory.

The script writes that resolved instance tree under `src/<Service>/`. Each
instance gets a directory with `init.<Class>.inst.json`; scripts also get
`init.luau`, `init.server.luau`, or `init.client.luau`. This carries over class,
name, script source, supported properties, attributes, and tags. Instance names
are encoded for safe filenames, and duplicate or case-colliding names get
separate directories. `StarterPlayerScripts` and `StarterCharacterScripts`
become their own sync targets. Rojo's `servePlaceIds` are retained.

The output also includes:

- `project.yml`, using Verde's default local address and port.
- `migration-report.txt`, with conversion warnings and next steps.
- `rojo-snapshot.rbxlx`, the original Rojo build for reference and recovery.
- `rojo-build.log`, including any warnings from Rojo.

## Limits

This creates a resolved copy. Continue editing the new `src/` tree; the original
Rojo files are independent of it. Tool configs, package manifests, non-instance
files, and build steps must be copied and adjusted separately. XML parsing may
normalize script line endings.

Verde cannot currently store service properties or properties pointing at other
instances. The report flags differences it can read; the Rojo snapshot retains
the built input, including unsupported data. Camera and Terrain instances are
skipped. Unknown classes stop the conversion so you can update the tools first.

Rojo's live-sync behavior, including `$ignoreUnknownInstances`, is replaced by
Verde's rules. Content present only in Studio is not part of the Rojo build;
review the initial sync before choosing a side. Model/plugin projects whose root
is not a `DataModel` are currently rejected.

## Tests

The Go integration tests run the actual Rojo and Lune executables, load the
converted output through Verde, and build a place from it:

```sh
cd executable
go test ./internal/project -run RojoMigration
```

They skip when Rojo or Lune is unavailable.
