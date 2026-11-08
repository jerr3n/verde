// SPDX-License-Identifier: MPL-2.0

package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

// newConfig writes files and a project.yml to a temporary folder and loads the config.
func newConfig(t *testing.T, files map[string]string, yml string) *config.Cfg {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	configPath := filepath.Join(dir, config.DefaultPath)
	if err := os.WriteFile(configPath, []byte("serve: {port: 1234}\n"+yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// byPath returns the tree's instances keyed by path, like "game/W/Main".
func byPath(tr *tree.Tree) map[string]protocol.Instance {
	out := map[string]protocol.Instance{}
	paths := map[protocol.Ref]string{}
	for _, inst := range tr.All() {
		full := inst.Name
		if inst.Parent != "" {
			full = paths[inst.Parent] + "/" + inst.Name
		}
		paths[inst.ID] = full
		out[full] = inst
	}
	return out
}

func sortedPaths(instances map[string]protocol.Instance) []string {
	var out []string
	for full := range instances {
		out = append(out, full)
	}
	slices.Sort(out)
	return out
}

const syncW = "sync:\n  - {instance: W, path: s}\n"

func TestClassify(t *testing.T) {
	tests := []struct {
		file, name, class string
		ok                bool
	}{
		{"Main.server.luau", "Main", "Script", true},
		{"Input.client.luau", "Input", "LocalScript", true},
		{"Util.luau", "Util", "ModuleScript", true},
		{"init.server.luau", "init", "Script", true},
		{"a.b.luau", "a.b", "ModuleScript", true},
		{"README.md", "", "", false},
		{"Main.LUAU", "", "", false},
		{".luau", "", "", false},
		{".server.luau", "", "Script", false},
		{".client.luau", "", "LocalScript", false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			name, class, ok := classify(tt.file)
			if ok != tt.ok || (ok && (name != tt.name || class != tt.class)) {
				t.Errorf("got (%q, %q, %v), want (%q, %q, %v)", name, class, ok, tt.name, tt.class, tt.ok)
			}
		})
	}
}

func TestIsInit(t *testing.T) {
	for file, want := range map[string]bool{
		"init.luau":        true,
		"init.server.luau": true,
		"init.client.luau": true,
		"Init.luau":        false,
		"init.md":          false,
		"myindex.luau":     false,
	} {
		if got := isInit(file); got != want {
			t.Errorf("isInit(%q) = %v, want %v", file, got, want)
		}
	}
}

func TestNewRef(t *testing.T) {
	// The same lowercase form the plugin generates for its own Refs.
	form := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	first, second := newRef(), newRef()
	if !form.MatchString(string(first)) {
		t.Errorf("newRef() = %q", first)
	}
	if first == second {
		t.Errorf("two calls both returned %q", first)
	}
}

func TestLoadFileFormat(t *testing.T) {
	cfg := newConfig(t, map[string]string{
		"s/Main.server.luau":      "print('main')\n",
		"s/Input.client.luau":     "input",
		"s/Util.luau":             "util",
		"s/Init.luau":             "capital",
		"s/README.md":             "skipped",
		"s/lib/Deep.luau":         "deep",
		"s/empty/.gitkeep":        "",
		"s/Shop/init.server.luau": "shop",
		"s/Shop/Prices.luau":      "prices",
		"s/Mod/init.luau":         "mod",
		"s/Gui/init.client.luau":  "gui",
	}, syncW)

	tr, _, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := byPath(tr)

	want := map[string]string{
		"game":               "DataModel",
		"game/W":             "W",
		"game/W/Main":        "Script",
		"game/W/Input":       "LocalScript",
		"game/W/Util":        "ModuleScript",
		"game/W/Init":        "ModuleScript",
		"game/W/lib":         "Folder",
		"game/W/lib/Deep":    "ModuleScript",
		"game/W/empty":       "Folder",
		"game/W/Shop":        "Script",
		"game/W/Shop/Prices": "ModuleScript",
		"game/W/Mod":         "ModuleScript",
		"game/W/Gui":         "LocalScript",
	}
	if len(got) != len(want) {
		t.Errorf("paths = %v", sortedPaths(got))
	}
	for full, class := range want {
		if got[full].Class != class {
			t.Errorf("%s: class = %q, want %q", full, got[full].Class, class)
		}
	}

	for full, source := range map[string]string{
		"game/W/Main": `"print('main')\n"`,
		"game/W/Shop": `"shop"`,
	} {
		value := got[full].Props["Source"]
		if value.Type != "String" || string(value.Value) != source {
			t.Errorf("%s: Source = %+v (%s), want %s", full, value, value.Value, source)
		}
	}
	if props := got["game/W/lib"].Props; props != nil {
		t.Errorf("a Folder shouldn't have props: %+v", props)
	}
}

func TestLoadListsParentsFirstInSortedOrder(t *testing.T) {
	tr, _, err := Load(newConfig(t, map[string]string{
		"s/b.luau":   "",
		"s/a.luau":   "",
		"s/c/x.luau": "",
	}, syncW))
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	for _, inst := range tr.All() {
		names = append(names, inst.Name)
	}
	if got, want := strings.Join(names, ","), "game,W,a,b,c,x"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}

func TestMounts(t *testing.T) {
	tr, mounts, err := Load(newConfig(t, nil, `
sync:
  - {instance: StarterPlayer/StarterPlayerScripts, path: a}
  - {instance: StarterPlayer/StarterCharacterScripts, path: b}
  - {instance: Workspace, path: c}
`))
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(mounts)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{
		`"path":[],"sync":false`,
		`"path":["StarterPlayer"],"sync":false`,
		`"path":["StarterPlayer","StarterPlayerScripts"],"sync":true`,
		`"path":["StarterPlayer","StarterCharacterScripts"],"sync":true`,
		`"path":["Workspace"],"sync":true`,
	} {
		if strings.Count(text, want) != 1 {
			t.Errorf("want %s exactly once in %s", want, text)
		}
	}

	// Every mount is in the tree, so a snapshot's instances include them.
	if len(mounts) != 5 || tr.Len() != 5 {
		t.Errorf("%d mounts and %d instances, want 5 of each", len(mounts), tr.Len())
	}
	for _, mount := range mounts {
		if _, ok := tr.Get(mount.ID); !ok {
			t.Errorf("mount %v isn't in the tree", mount.Path)
		}
	}
}

func TestSampleConfig(t *testing.T) {
	sample, err := os.ReadFile(filepath.Join("..", "..", "test", "project.yml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), config.DefaultPath)
	if err := os.WriteFile(path, sample, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	tr, mounts, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// The DataModel, StarterPlayer, and one for each of the sample's sync entries.
	want := 2 + len(cfg.Sync)
	if len(mounts) != want || tr.Len() != want {
		t.Errorf("%d mounts and %d instances, want %d of each", len(mounts), tr.Len(), want)
	}
}

func TestIgnore(t *testing.T) {
	files := map[string]string{
		"s/Keep.luau":          "",
		"s/x.tmp.luau":         "",
		"s/A/Inner.luau":       "",
		"s/B/Inner.luau":       "",
		"s/C/init.luau":        "",
		"s/C/Inner.luau":       "",
		"s/D/init.luau":        "",
		"s/D/init.server.luau": "",
	}

	tests := []struct {
		name, ignore string
		gone, kept   []string
	}{
		{
			"paths match files",
			`{paths: ["**/*.tmp.luau", "s/D/init.luau"]}`,
			[]string{"game/W/x.tmp"},
			[]string{"game/W/Keep", "game/W/D"},
		},
		{
			"paths match folders and their contents",
			`{paths: ["s/A", "s/D/init.luau"]}`,
			[]string{"game/W/A", "game/W/A/Inner"},
			[]string{"game/W/B/Inner"},
		},
		{
			"instances",
			`{paths: ["s/D/**"], instances: [W/A, W/Keep, W/C]}`,
			[]string{"game/W/A", "game/W/A/Inner", "game/W/Keep", "game/W/C", "game/W/C/Inner"},
			[]string{"game/W/B/Inner"},
		},
		{
			"classes",
			`{paths: ["s/D/**"], classes: [ModuleScript]}`,
			[]string{"game/W/Keep", "game/W/B/Inner", "game/W/C", "game/W/C/Inner"},
			[]string{"game/W/B"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, _, err := Load(newConfig(t, files, syncW+"ignore: "+tt.ignore+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			got := byPath(tr)
			for _, full := range tt.gone {
				if _, ok := got[full]; ok {
					t.Errorf("%s should be ignored", full)
				}
			}
			for _, full := range tt.kept {
				if _, ok := got[full]; !ok {
					t.Errorf("%s is missing from %v", full, sortedPaths(got))
				}
			}
		})
	}
}

// An ignored init file doesn't make its folder a script.
func TestIgnoredInitFile(t *testing.T) {
	tr, _, err := Load(newConfig(t, map[string]string{"s/F/init.luau": ""}, syncW+`ignore: {paths: ["s/F/init.luau"]}`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := byPath(tr)["game/W/F"].Class; got != "Folder" {
		t.Errorf("class = %q, want Folder", got)
	}
}

func TestMissingFolderIsEmpty(t *testing.T) {
	tr, _, err := Load(newConfig(t, nil, syncW))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Len() != 2 {
		t.Errorf("Len = %d, want 2", tr.Len())
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		ignore string
		want   string
	}{
		{name: "two init files", files: map[string]string{"s/F/init.luau": "", "s/F/init.server.luau": ""}, want: "has both"},
		{name: "init file in a mount", files: map[string]string{"s/init.luau": ""}, want: "init file"},
		{name: "invalid UTF-8", files: map[string]string{"s/A.luau": "\xff"}, want: "UTF-8"},
		{name: "invalid UTF-8 in an init file", files: map[string]string{"s/F/init.luau": "\xff"}, want: "UTF-8"},
		{name: "file as a sync path", files: map[string]string{"s": "not a folder"}, want: "not a folder"},
		{name: "file and init file collide", files: map[string]string{"s/Foo.luau": "", "s/Foo/init.luau": ""}, want: "both a ModuleScript called Foo"},
		{name: "bad ignore pattern", ignore: `ignore: {paths: ["a["]}`, want: "not a valid pattern"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, mounts, err := Load(newConfig(t, tt.files, syncW+tt.ignore+"\n"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if tr != nil || mounts != nil {
				t.Errorf("got a partial result alongside the error")
			}
		})
	}
}

// Different classes can share a name, the same as in Studio.
func TestSameNameDifferentClass(t *testing.T) {
	tr, _, err := Load(newConfig(t, map[string]string{"s/Foo.luau": "", "s/Foo.server.luau": "", "s/Foo/Bar.luau": ""}, syncW))
	if err != nil {
		t.Fatal(err)
	}
	if tr.Len() != 6 {
		t.Errorf("Len = %d, want 6", tr.Len())
	}
}

func TestSymlinksInsideAreSkipped(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "Secret.luau"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := newConfig(t, map[string]string{"s/A.luau": ""}, syncW)
	for name, target := range map[string]string{
		"s/File.luau": filepath.Join(outside, "Secret.luau"),
		"s/init.luau": filepath.Join(outside, "Secret.luau"),
		"s/Dir":       outside,
	} {
		if err := os.Symlink(target, filepath.Join(cfg.Root, name)); err != nil {
			t.Fatal(err)
		}
	}

	tr, _, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sortedPaths(byPath(tr)), []string{"game", "game/W", "game/W/A"}; !slices.Equal(got, want) {
		t.Errorf("paths = %v, want %v", got, want)
	}
}

func TestSymlinkedSyncPathOutsideIsRejected(t *testing.T) {
	cfg := newConfig(t, nil, "sync:\n  - {instance: W, path: link/inner}\n")
	if err := os.Symlink(t.TempDir(), filepath.Join(cfg.Root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(cfg); err == nil {
		t.Fatal("Load followed a symlink out of the project")
	}
}
