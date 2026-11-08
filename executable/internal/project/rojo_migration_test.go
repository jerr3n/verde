// SPDX-License-Identifier: MPL-2.0

package project_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jerr3n/verde/executable/internal/build"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/project"
	"github.com/jerr3n/verde/executable/internal/protocol"
)

func migrationScript(t *testing.T) (string, string) {
	t.Helper()
	lune, err := exec.LookPath("lune")
	if err != nil {
		t.Skip("Rojo migration integration tests require Lune")
	}
	if _, err := exec.LookPath("rojo"); err != nil {
		t.Skip("Rojo migration integration tests require Rojo")
	}
	script, err := filepath.Abs("../../../scripts/migrate-rojo.luau")
	if err != nil {
		t.Fatal(err)
	}
	return lune, script
}

func migrationFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func runMigration(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	lune, script := migrationScript(t)
	cmd := exec.Command(lune, append([]string{"run", script}, args...)...)
	cmd.Dir = root // Exercise invoking the script outside the Verde checkout.
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRojoMigrationLoadsAndBuilds(t *testing.T) {
	migrationScript(t)
	dir := t.TempDir()
	const server = "-- keep CRLF and source text\r\nprint('server')\r\n"
	migrationFiles(t, dir, map[string]string{
		"default.project.jsonc": `{
			// JSONC, directory paths and nested projects are resolved by Rojo.
			"name": "Game: #1",
			"servePlaceIds": [1234567890],
			"globIgnorePaths": ["**/ignored.lua"],
			"tree": {
				"$className": "DataModel",
				"ReplicatedStorage": {
					"Shared": {"$path": "src/shared"},
					"Package": {"$path": "package/default.project.json"},
					"Links": {"$path": "links.rbxmx"}
				},
				"ServerScriptService": {"Main": {"$path": "src/server"}},
				"StarterPlayer": {
					"StarterPlayerScripts": {"Client": {"$path": "src/client.client.lua"}}
				},
				"Workspace": {
					"A/B%": {"$className": "Part", "$properties": {
						"Anchored": true, "Position": [10, 20, 30], "Size": [2, 4, 8],
						"Material": "Neon", "Shape": "Ball", "Color": [0.2, 0.4, 0.6],
						"Tags": ["Migrated"], "Attributes": {"Score": {"Float64": 42}}
					}},
					"init": {"$className": "StringValue", "$properties": {"Value": "reserved"}},
					"Transform": {"$className": "Part", "$properties": {"CFrame": [1,2,3,1,0,0,0,1,0,0,0,1]}},
					"Rotated": {"$className": "Part", "$properties": {"Orientation": [0,90,0]}},
					"Plain": {"$className": "Part"}
				},
				"Lighting": {"$properties": {"Brightness": 4}}
			}
		}`,
		"links.rbxmx": `<roblox version="4"><Item class="Model" referent="model">
			<Properties><string name="Name">Links</string></Properties>
			<Item class="Part" referent="target"><Properties><string name="Name">Target</string></Properties></Item>
			<Item class="ObjectValue" referent="ref"><Properties><string name="Name">Reference</string><Ref name="Value">target</Ref></Properties></Item>
		</Item></roblox>`,
		"src/server/init.server.lua": server,
		"src/server/init.meta.json":  `{"properties":{"Disabled":true}}`,
		"src/server/Child.lua":       "return 'child'\n",
		"src/client.client.lua":      "print('client')\n",
		"src/shared/init.lua":        "return {}\n",
		"src/shared/Util.luau":       "return 42\n",
		"src/shared/ignored.lua":     "error('must not be imported')",
		"package/default.project.json": `{
			"name":"Package", "tree":{"$className":"Model", "Bundle":{"$path":"Bundle.model.json"}}
		}`,
		"package/Bundle.model.json": `{"className":"Model", "children":[
			{"name":"Same", "className":"StringValue", "properties":{"Value":"first"}},
			{"name":"Same", "className":"StringValue", "properties":{"Value":"second"}},
			{"name":"same", "className":"StringValue", "properties":{"Value":"third"}}
		]}`,
	})
	out, err := runMigration(t, dir)
	if err != nil {
		t.Fatalf("migration: %v\n%s", err, out)
	}
	cfg, err := config.Load(filepath.Join(dir, "verde", config.DefaultPath))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "Game: #1" || cfg.Conflicts.OnConnect != config.OnConnectInteractive ||
		len(cfg.Serve.PlaceIds) != 1 || cfg.Serve.PlaceIds[0] != 1234567890 {
		t.Fatalf("unexpected generated config: %+v", cfg)
	}
	tr, mounts, err := project.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	instances := tr.All()
	var part, main, client, transformed, rotated protocol.Instance
	var shared, module, duplicateValues, reserved int
	for _, inst := range instances {
		switch inst.Name {
		case "A/B%":
			part = inst
		case "Transform":
			transformed = inst
		case "Rotated":
			rotated = inst
		case "Main":
			main = inst
		case "Client":
			client = inst
		case "Shared":
			if inst.Class == "ModuleScript" {
				shared++
			}
		case "Util":
			if inst.Class == "ModuleScript" {
				module++
			}
		case "Same", "same":
			if inst.Class == "StringValue" {
				duplicateValues++
			}
		case "init":
			if inst.Class == "StringValue" {
				reserved++
			}
		case "Reference":
			if _, exists := inst.Props["Value"]; exists {
				t.Fatal("unsupported reference was written to disk")
			}
		case "ignored":
			t.Fatal("Rojo's ignored script was imported")
		}
	}
	if shared != 1 || module != 1 || duplicateValues != 3 || reserved != 1 {
		t.Fatalf("wrong instance counts: shared=%d module=%d duplicates=%d init=%d", shared, module, duplicateValues, reserved)
	}
	check := func(inst protocol.Instance, prop, want string) {
		t.Helper()
		got := inst.Props[prop]
		var decoded any
		if err := json.Unmarshal(got.Value, &decoded); err != nil {
			t.Fatalf("%s.%s: %v", inst.Name, prop, err)
		}
		canonical, _ := json.Marshal(decoded)
		if string(canonical) != want {
			t.Errorf("%s.%s = %s, want %s", inst.Name, prop, canonical, want)
		}
	}
	if main.Class != "Script" || client.Class != "LocalScript" || part.Class != "Part" {
		t.Fatalf("classes: main=%s client=%s part=%s", main.Class, client.Class, part.Class)
	}
	source, _ := json.Marshal(strings.ReplaceAll(server, "\r\n", "\n"))
	check(main, "Source", string(source))
	check(main, "Disabled", "true")
	check(part, "Anchored", "true")
	check(part, "CFrame", "[10,20,30,1,0,0,0,1,0,0,0,1]")
	check(transformed, "CFrame", "[1,2,3,1,0,0,0,1,0,0,0,1]")
	var rotation []float64
	if err := json.Unmarshal(rotated.Props["CFrame"].Value, &rotation); err != nil || len(rotation) != 12 || rotation[5] < 0.99 || rotation[9] > -0.99 {
		t.Fatalf("Orientation was not converted into CFrame: %v, %v", rotation, err)
	}
	check(part, "Size", "[2,4,8]")
	check(part, "Material", `["Material","Neon"]`)
	check(part, "Shape", `["PartType","Ball"]`)
	check(part, "$tags", `["Migrated"]`)
	check(part, "$attributes", `[["Score",{"t":"Number","v":42}]]`)
	if _, err := os.Stat(filepath.Join(dir, "verde", "src", "ServerScriptService", "Main", "init.server.luau")); err != nil {
		t.Errorf("missing init script: %v", err)
	}
	result, err := build.WriteFile(filepath.Join(dir, "converted.rbxlx"), tr, mounts, "")
	if err != nil || len(result.Warnings) > 0 {
		t.Fatalf("build: %v, warnings: %v", err, result.Warnings)
	}
	if !strings.Contains(out, "Reference: Value points to another instance") {
		t.Errorf("missing reference warning: %s", out)
	}
	if !strings.Contains(out, "Lighting: Brightness") {
		t.Errorf("missing service property warning: %s", out)
	}
	original, err := os.ReadFile(filepath.Join(dir, "src", "server", "init.server.lua"))
	if err != nil || string(original) != server {
		t.Fatalf("original script changed: %v, %q", err, original)
	}
	for _, name := range []string{"rojo-snapshot.rbxlx", "migration-report.txt", "rojo-build.log"} {
		if _, err := os.Stat(filepath.Join(dir, "verde", name)); err != nil {
			t.Error(err)
		}
	}
}

func TestRojoMigrationDryRunAndFailures(t *testing.T) {
	migrationScript(t)
	for _, kind := range []string{"dry run", "existing destination", "build failure", "conversion failure", "model project"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			body := `{"name":"Game", "tree":{"$className":"DataModel", "Workspace":{"$className":"Workspace"}}}`
			args := []string{".", "--output", "converted/"}
			switch kind {
			case "dry run":
				args = append(args, "--dry-run")
			case "existing destination":
				migrationFiles(t, dir, map[string]string{"converted/keep.txt": "keep"})
			case "build failure":
				body = `{"name":"Game", "tree":{"$className":"DataModel", "Workspace":{"Bad":{"$className":"Part", "$properties":{"Size":"invalid"}}}}}`
			case "conversion failure":
				body = `{"name":"Game", "tree":{"$className":"DataModel", "Custom":{"$className":"Folder"}}}`
			case "model project":
				body = `{"name":"Game", "tree":{"$className":"Model"}}`
			}
			migrationFiles(t, dir, map[string]string{"default.project.json": body})
			out, err := runMigration(t, dir, args...)
			if (err == nil) != (kind == "dry run") {
				t.Fatalf("migration: %v\n%s", err, out)
			}
			if kind == "existing destination" {
				got, err := os.ReadFile(filepath.Join(dir, "converted", "keep.txt"))
				if err != nil || string(got) != "keep" {
					t.Fatalf("destination changed: %v, %q", err, got)
				}
			} else if _, err := os.Lstat(filepath.Join(dir, "converted")); !os.IsNotExist(err) {
				t.Fatalf("unexpected output after %s: %v", kind, err)
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, "*.migrating-*"))
			if len(leftovers) != 0 {
				t.Errorf("temporary folders left behind: %v", leftovers)
			}
		})
	}
}
