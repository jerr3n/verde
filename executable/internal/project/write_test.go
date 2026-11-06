package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

// fixture is a loaded project that patches can be applied to, as the session does.
type fixture struct {
	t      *testing.T
	cfg    *config.Cfg
	tree   *tree.Tree
	mounts []protocol.Mount
}

func newFixture(t *testing.T, files map[string]string, yml string) *fixture {
	t.Helper()
	cfg := newConfig(t, files, yml)
	tr, mounts, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t, cfg, tr, mounts}
}

// ref finds an instance's Ref by path, like "game/W/Main".
func (f *fixture) ref(full string) protocol.Ref {
	f.t.Helper()
	inst, ok := byPath(f.tree)[full]
	if !ok {
		f.t.Fatalf("no instance at %s, have %v", full, sortedPaths(byPath(f.tree)))
	}
	return inst.ID
}

// apply applies a patch that should cause no errors, then checks the rule the whole
// feature rests on: scanning what was just written must find nothing to send back.
func (f *fixture) apply(p protocol.Patch) protocol.Patch {
	f.t.Helper()
	applied, errs := Apply(f.cfg, f.tree, f.mounts, p)
	if len(errs) > 0 {
		f.t.Fatalf("Apply: %v", errs)
	}
	f.assertNoEcho()
	return applied
}

func (f *fixture) assertNoEcho() {
	f.t.Helper()
	next, _, err := Load(f.cfg)
	if err != nil {
		f.t.Fatalf("rescan: %v", err)
	}
	if patch := tree.Diff(f.tree, next); !patch.Empty() {
		f.t.Errorf("rescan would send a patch: %+v", patch)
	}
}

func (f *fixture) assertFile(name, want string) {
	f.t.Helper()
	got, err := os.ReadFile(filepath.Join(f.cfg.Root, name))
	if err != nil {
		f.t.Errorf("%s: %v (have %v)", name, err, f.files())
		return
	}
	if string(got) != want {
		f.t.Errorf("%s = %q, want %q", name, got, want)
	}
}

func (f *fixture) assertMissing(name string) {
	f.t.Helper()
	if _, err := os.Lstat(filepath.Join(f.cfg.Root, name)); err == nil {
		f.t.Errorf("%s still exists", name)
	}
}

// files lists every file in the project except the config and the trash.
func (f *fixture) files() []string {
	var out []string
	filepath.WalkDir(f.cfg.Root, func(full string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.cfg.Root, full)
		if d.IsDir() && rel == ".verde" {
			return filepath.SkipDir
		}
		if !d.IsDir() && rel != config.DefaultPath {
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	slices.Sort(out)
	return out
}

// trashed returns the text of every trashed file ending in suffix.
func (f *fixture) trashed(suffix string) []string {
	var out []string
	filepath.WalkDir(filepath.Join(f.cfg.Root, ".verde/trash"), func(full string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(filepath.ToSlash(full), suffix) {
			text, _ := os.ReadFile(full)
			out = append(out, string(text))
		}
		return nil
	})
	return out
}

func script(id protocol.Ref, parent protocol.Ref, name, class, text string) protocol.Instance {
	return protocol.Instance{ID: id, Parent: parent, Name: name, Class: class, Props: srcProps(text)}
}

func srcProps(text string) map[string]protocol.Value {
	encoded, _ := json.Marshal(text)
	return map[string]protocol.Value{"Source": {Type: "String", Value: encoded}}
}

func TestApplyAddsEachClass(t *testing.T) {
	f := newFixture(t, map[string]string{"s/.keep": ""}, syncW)
	w := f.ref("game/W")

	f.apply(protocol.Patch{Added: []protocol.Instance{
		script("a", w, "Main", "Script", "server"),
		script("b", w, "Input", "LocalScript", "client"),
		script("c", w, "Util", "ModuleScript", "module"),
		{ID: "d", Parent: w, Name: "lib", Class: "Folder"},
		script("e", "d", "Inner", "ModuleScript", "inner"),
	}})

	f.assertFile("s/Main.server.luau", "server")
	f.assertFile("s/Input.client.luau", "client")
	f.assertFile("s/Util.luau", "module")
	f.assertFile("s/lib/Inner.luau", "inner")
}

func TestApplyCreatesMissingSyncFolder(t *testing.T) {
	f := newFixture(t, nil, syncW)
	f.apply(protocol.Patch{Added: []protocol.Instance{script("a", f.ref("game/W"), "Main", "Script", "x")}})
	f.assertFile("s/Main.server.luau", "x")
}

func TestApplyWritesScriptWithoutSource(t *testing.T) {
	f := newFixture(t, nil, syncW)
	// apply fails if the next scan finds a Source the tree doesn't have.
	f.apply(protocol.Patch{Added: []protocol.Instance{{ID: "a", Parent: f.ref("game/W"), Name: "Empty", Class: "ModuleScript"}}})
	f.assertFile("s/Empty.luau", "")
}

func TestApplyUpdatesSource(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "old"}, syncW)

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/Main"), Props: srcProps("new\n")}}})

	f.assertFile("s/Main.server.luau", "new\n")
	if got := f.files(); !slices.Equal(got, []string{"s/Main.server.luau"}) {
		t.Errorf("files = %v, a temporary file may be left behind", got)
	}
}

func TestApplyKeepsFileMode(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "old"}, syncW)
	full := filepath.Join(f.cfg.Root, "s/Main.server.luau")
	if err := os.Chmod(full, 0o600); err != nil {
		t.Fatal(err)
	}

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/Main"), Props: srcProps("new")}}})

	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestApplyLeavesUnchangedFileAlone(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "same"}, syncW)
	full := filepath.Join(f.cfg.Root, "s/Main.server.luau")
	before, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/Main"), Props: srcProps("same")}}})

	after, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("the file was replaced even though its text didn't change")
	}
}

func TestApplyRenames(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/Main.server.luau":       "main",
		"s/Shop/index.server.luau": "shop",
		"s/Shop/Item.luau":         "item",
		"s/lib/Util.luau":          "util",
		"s/lib/README.md":          "not an instance",
	}, syncW)

	f.apply(protocol.Patch{Updated: []protocol.Update{
		{ID: f.ref("game/W/Main"), Name: "Entry"},
		{ID: f.ref("game/W/Shop"), Name: "Store"},
		{ID: f.ref("game/W/lib"), Name: "shared"},
	}})

	f.assertMissing("s/Main.server.luau")
	f.assertFile("s/Entry.server.luau", "main")
	f.assertMissing("s/Shop")
	f.assertFile("s/Store/index.server.luau", "shop")
	f.assertFile("s/Store/Item.luau", "item")
	f.assertMissing("s/lib")
	f.assertFile("s/shared/Util.luau", "util")
	f.assertFile("s/shared/README.md", "not an instance")
}

func TestApplyRenameAndSourceTogether(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "old"}, syncW)
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/Main"), Name: "Entry", Props: srcProps("new")}}})
	f.assertFile("s/Entry.server.luau", "new")
	f.assertMissing("s/Main.server.luau")
}

func TestApplyMoves(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/a/One.luau":     "one",
		"s/a/sub/Two.luau": "two",
		"s/b/.keep":        "",
	}, syncW)

	f.apply(protocol.Patch{Updated: []protocol.Update{
		{ID: f.ref("game/W/a/One"), Parent: f.ref("game/W/b")},
		{ID: f.ref("game/W/a/sub"), Parent: f.ref("game/W/b")},
	}})

	f.assertFile("s/b/One.luau", "one")
	f.assertFile("s/b/sub/Two.luau", "two")
	f.assertMissing("s/a/One.luau")
	f.assertMissing("s/a/sub")
}

func TestApplyMoveBetweenMounts(t *testing.T) {
	f := newFixture(t, map[string]string{"one/Main.luau": "x"}, "sync:\n  - {instance: A, path: one}\n  - {instance: B, path: two}\n")
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/A/Main"), Parent: f.ref("game/B")}}})
	f.assertFile("two/Main.luau", "x")
	f.assertMissing("one/Main.luau")
}

func TestApplyScriptBecomesFolderWhenItGetsAChild(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Shop.server.luau": "shop"}, syncW)

	f.apply(protocol.Patch{Added: []protocol.Instance{script("a", f.ref("game/W/Shop"), "Item", "ModuleScript", "item")}})

	f.assertMissing("s/Shop.server.luau")
	f.assertFile("s/Shop/index.server.luau", "shop")
	f.assertFile("s/Shop/Item.luau", "item")

	// Its Source now goes to the new place.
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/Shop"), Props: srcProps("changed")}}})
	f.assertFile("s/Shop/index.server.luau", "changed")

	// It stays a folder without children, which is still a valid project.
	f.apply(protocol.Patch{Removed: []protocol.Ref{"a"}})
	f.assertFile("s/Shop/index.server.luau", "changed")
}

func TestApplyScriptWontMergeWithFolderOfTheSameName(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/Foo.luau":     "script",
		"s/Foo/Bar.luau": "bar",
	}, syncW)

	inst, ok := byPath(f.tree)["game/W/Foo"]
	if !ok {
		t.Fatal("no Foo")
	}
	var scriptRef protocol.Ref
	for _, id := range f.tree.Children(inst.Parent) {
		if other, _ := f.tree.Get(id); other.Class == "ModuleScript" {
			scriptRef = id
		}
	}

	f.apply(protocol.Patch{Added: []protocol.Instance{
		script("kid", scriptRef, "Kid", "ModuleScript", "kid"),
	}})

	// the folder keeps its name, the script gets its own folder w/ its name inside
	dir := "s/Foo" + tagFor(scriptRef)
	f.assertFile("s/Foo/Bar.luau", "bar")
	f.assertMissing("s/Foo/Kid.luau")
	f.assertMissing("s/Foo.luau")
	f.assertFile(dir+"/index.luau", "script")
	f.assertFile(dir+"/Kid.luau", "kid")
	f.assertFile(dir+"/index.ModuleScript.inst.json", "{\n  \"id\": \""+string(scriptRef)+"\",\n  \"name\": \"Foo\"\n}\n")
}

func TestApplyRemovesToTrash(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/Main.server.luau": "main",
		"s/lib/Util.luau":    "util",
		"s/lib/README.md":    "keep me",
		"s/lib/sub/Two.luau": "two",
	}, syncW)
	lib, sub, two := f.ref("game/W/lib"), f.ref("game/W/lib/sub"), f.ref("game/W/lib/sub/Two")
	util := f.ref("game/W/lib/Util")
	main := f.ref("game/W/Main")

	// Listed like the plugin lists them: descendants too, children first.
	applied := f.apply(protocol.Patch{Removed: []protocol.Ref{two, util, sub, lib, main}})

	f.assertMissing("s/lib")
	f.assertMissing("s/Main.server.luau")
	if len(applied.Removed) != 5 {
		t.Errorf("forwarded %d removals, want 5", len(applied.Removed))
	}
	for name, want := range map[string]string{
		"s/Main.server.luau": "main",
		"s/lib/Util.luau":    "util",
		"s/lib/README.md":    "keep me",
		"s/lib/sub/Two.luau": "two",
	} {
		if got := f.trashed(name); !slices.Equal(got, []string{want}) {
			t.Errorf("trash has %s = %v, want %q", name, got, want)
		}
	}
	if text, err := os.ReadFile(filepath.Join(f.cfg.Root, trashDir, ".gitignore")); err != nil || string(text) != "*\n" {
		t.Errorf("trash .gitignore = %q, %v", text, err)
	}
}

func TestApplyRemoveUnknownIsNoOp(t *testing.T) {
	f := newFixture(t, nil, syncW)
	f.apply(protocol.Patch{Removed: []protocol.Ref{"nope"}})
}

func TestApplyDangerousNamesStayInsideAndRoundTrip(t *testing.T) {
	names := []string{
		"../../escape", "..", ".", "/", "a/b", `a\b`, "a:b", "What?", "*", `q"q`, "<x>", "p|p",
		"100%", "%2F", ".git", "index", "x.server", "x.client", "CON", "nul", "COM1.thing",
		"trailing.", "trailing ", "\x01ctl", "ümlaut", "日本語", "x.luau",
	}
	for _, class := range []string{"Script", "LocalScript", "ModuleScript", "Folder"} {
		t.Run(class, func(t *testing.T) {
			f := newFixture(t, nil, syncW)
			w := f.ref("game/W")
			var added []protocol.Instance
			for i, name := range names {
				inst := protocol.Instance{ID: protocol.Ref(rune('A'+i)) + "-id", Parent: w, Name: name, Class: class}
				if class != "Folder" {
					inst.Props = srcProps(name)
				}
				added = append(added, inst)
			}
			f.apply(protocol.Patch{Added: added}) // fails if a name comes back differently

			next, _, err := Load(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(next.Children(next.Children(next.Root())[0])); got != len(names) {
				t.Errorf("scan found %d instances, want %d", got, len(names))
			}

			parent := filepath.Dir(f.cfg.Root)
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != filepath.Base(f.cfg.Root) {
					t.Errorf("%s escaped the project", entry.Name())
				}
			}
		})
	}
}

func TestApplyRenameExistingFileWithUnsafeName(t *testing.T) {
	// Not something Verde would write, but valid on Linux, and it has to be found.
	f := newFixture(t, map[string]string{"s/What?.luau": "x"}, syncW)

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/What?"), Props: srcProps("y")}}})
	f.assertFile("s/What?.luau", "y")

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/What?"), Name: "Fine"}}})
	f.assertFile("s/Fine.luau", "y")
	f.assertMissing("s/What?.luau")
}

func TestApplyWritesNonScripts(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")

	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "part", Parent: w, Name: "Lamp", Class: "Part", Props: boolProp("Anchored", true)},
		script("inner", "part", "Glow", "Script", "glow"),
	}})

	f.assertFile("s/Lamp/index.Part.inst.json", "{\n  \"props\": {\n    \"Anchored\": {\"t\":\"Bool\",\"v\":true}\n  }\n}\n")
	f.assertFile("s/Lamp/Glow.server.luau", "glow")

	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "inner", Props: srcProps("changed")}}})
	f.assertFile("s/Lamp/Glow.server.luau", "changed")
}

func TestApplyPropsOnScriptsGetTheirOwnFile(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.server.luau": "x"}, syncW)
	id := f.ref("game/W/Main")
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: id, Props: boolProp("Disabled", true)}}})
	f.assertFile("s/Main.server.luau", "x")
	f.assertFile("s/Main.Script.inst.json", "{\n  \"props\": {\n    \"Disabled\": {\"t\":\"Bool\",\"v\":true}\n  }\n}\n")
}

func TestApplyMoveIntoAndOutOfNonScript(t *testing.T) {
	f := newFixture(t, map[string]string{
		"s/Main.luau":    "main",
		"s/lib/Sub.luau": "sub",
	}, syncW)
	w := f.ref("game/W")
	main, lib := f.ref("game/W/Main"), f.ref("game/W/lib")

	f.apply(protocol.Patch{Added: []protocol.Instance{{ID: "part", Parent: w, Name: "Lamp", Class: "Part"}}})

	f.apply(protocol.Patch{Updated: []protocol.Update{
		{ID: main, Parent: "part"},
		{ID: lib, Parent: "part"},
	}})
	f.assertMissing("s/Main.luau")
	f.assertMissing("s/Lamp.Part.inst.json")
	f.assertFile("s/Lamp/index.Part.inst.json", "{}\n")
	f.assertFile("s/Lamp/Main.luau", "main")
	f.assertFile("s/Lamp/lib/Sub.luau", "sub")

	// moving out leaves the part as a folder, like a script that once had children
	f.apply(protocol.Patch{Updated: []protocol.Update{
		{ID: main, Parent: w},
		{ID: lib, Parent: w},
	}})
	f.assertFile("s/Main.luau", "main")
	f.assertFile("s/lib/Sub.luau", "sub")
	f.assertFile("s/Lamp/index.Part.inst.json", "{}\n")
}

func TestApplySameNameSiblings(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")

	f.apply(protocol.Patch{Added: []protocol.Instance{
		script("first", w, "Dup", "ModuleScript", "first"),
		script("second", w, "Dup", "ModuleScript", "second"),
	}})
	second := "s/Dup" + tagFor("second")
	f.assertFile("s/Dup.luau", "first")
	f.assertFile(second+".luau", "second")
	f.assertFile(second+".ModuleScript.inst.json", "{\n  \"id\": \"second\",\n  \"name\": \"Dup\"\n}\n")
	f.assertFile("s/Dup.ModuleScript.inst.json", "{\n  \"id\": \"first\"\n}\n")

	// editing one doesn't touch the other's file
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "second", Props: srcProps("second2")}}})
	f.assertFile("s/Dup.luau", "first")
	f.assertFile(second+".luau", "second2")
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: "first", Props: srcProps("first2")}}})
	f.assertFile("s/Dup.luau", "first2")
	f.assertFile(second+".luau", "second2")

	// deleting the first leaves the second where it is, so its file doesn't change name in
	// git because of something that happened to another instance
	f.apply(protocol.Patch{Removed: []protocol.Ref{"first"}})
	f.assertMissing("s/Dup.luau")
	f.assertMissing("s/Dup.ModuleScript.inst.json")
	f.assertFile(second+".luau", "second2")
}

func TestApplyNamesDifferingOnlyByCase(t *testing.T) {
	f := newFixture(t, nil, syncW)
	w := f.ref("game/W")

	// they'd be the same file on windows and macos
	f.apply(protocol.Patch{Added: []protocol.Instance{
		script("a", w, "Foo", "ModuleScript", "a"),
		script("b", w, "foo", "ModuleScript", "b"),
	}})
	f.assertFile("s/Foo.luau", "a")
	f.assertMissing("s/foo.luau")
	f.assertFile("s/foo"+tagFor("b")+".luau", "b")
	if got := f.restart(); got["game/W/foo"].Props["Source"].Type != "String" || got["game/W/Foo"].Props["Source"].Type != "String" {
		t.Errorf("have %v", sortedPaths(got))
	}
}

func TestApplyCaseOnlyRename(t *testing.T) {
	f := newFixture(t, map[string]string{"s/foo.luau": "x"}, syncW)
	f.apply(protocol.Patch{Updated: []protocol.Update{{ID: f.ref("game/W/foo"), Name: "Foo"}}})
	f.assertFile("s/Foo.luau", "x")
}

func TestApplyDeleteThenAddSameNameInOnePatch(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.luau": "old"}, syncW)
	old := f.ref("game/W/Main")

	// The protocol applies added first, while the old one still has the name. That
	// sorts itself out by the end of the patch, so apply also checks there's no error.
	f.apply(protocol.Patch{
		Added:   []protocol.Instance{script("new", f.ref("game/W"), "Main", "ModuleScript", "new")},
		Removed: []protocol.Ref{old},
	})
	f.assertFile("s/Main.luau", "new")
	if f.tree.IsFileless("new") {
		t.Error("new should have its file")
	}
}

func TestApplySwappedNames(t *testing.T) {
	f := newFixture(t, map[string]string{"s/A.luau": "a", "s/B.luau": "b"}, syncW)
	a, b := f.ref("game/W/A"), f.ref("game/W/B")

	f.apply(protocol.Patch{Updated: []protocol.Update{
		{ID: a, Name: "B"},
		{ID: b, Name: "A"},
	}})
	f.assertFile("s/A.luau", "b")
	f.assertFile("s/B.luau", "a")
}

func TestApplyIgnoredPathStaysInTree(t *testing.T) {
	f := newFixture(t, nil, syncW+"ignore: {paths: [\"**/*.tmp\"]}\n")
	w := f.ref("game/W")

	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		{ID: "dir", Parent: w, Name: "cache.tmp", Class: "Folder"},
		script("kid", "dir", "Kid", "ModuleScript", "kid"),
	}})
	if len(errs) != 1 { // only for the Folder: its child's reason is the same
		t.Errorf("errs = %v, want one", errs)
	}
	f.assertMissing("s/cache.tmp")
	// Without the fileless mark, the scan wouldn't find it and Studio would delete it.
	f.assertNoEcho()
}

func TestApplyIgnoredInstanceStaysInTree(t *testing.T) {
	f := newFixture(t, nil, syncW+"ignore: {instances: [W/Secret], classes: [LocalScript]}\n")
	w := f.ref("game/W")

	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		script("a", w, "Secret", "ModuleScript", "a"),
		script("b", w, "Client", "LocalScript", "b"),
	}})
	if len(errs) != 2 {
		t.Errorf("errs = %v, want two", errs)
	}
	if got := f.files(); len(got) != 0 {
		t.Errorf("files = %v", got)
	}
	f.assertNoEcho()
}

func TestApplyEmptyNameStaysInTree(t *testing.T) {
	f := newFixture(t, nil, syncW)
	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		script("a", f.ref("game/W"), "", "ModuleScript", "a"),
	}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	f.assertNoEcho()
}

func TestApplyProtectsMounts(t *testing.T) {
	yml := "sync:\n  - {instance: P/S, path: s}\n"
	f := newFixture(t, map[string]string{"s/Main.luau": "main"}, yml)
	game, parent, mount := f.tree.Root(), f.ref("game/P"), f.ref("game/P/S")
	main := f.ref("game/P/S/Main")

	tests := []struct {
		name  string
		patch protocol.Patch
	}{
		{"remove sync mount", protocol.Patch{Removed: []protocol.Ref{mount}}},
		{"remove parent mount", protocol.Patch{Removed: []protocol.Ref{parent}}},
		{"remove root", protocol.Patch{Removed: []protocol.Ref{game}}},
		{"rename sync mount", protocol.Patch{Updated: []protocol.Update{{ID: mount, Name: "X"}}}},
		{"move sync mount", protocol.Patch{Updated: []protocol.Update{{ID: mount, Parent: game}}}},
		{"move out of sync", protocol.Patch{Updated: []protocol.Update{{ID: main, Parent: game}}}},
		{"add outside sync", protocol.Patch{Added: []protocol.Instance{script("a", parent, "A", "Script", "")}}},
		{"add at root", protocol.Patch{Added: []protocol.Instance{script("b", game, "B", "Script", "")}}},
		{"add to unknown parent", protocol.Patch{Added: []protocol.Instance{script("c", "nope", "C", "Script", "")}}},
		{"update unknown", protocol.Patch{Updated: []protocol.Update{{ID: "nope", Name: "X"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applied, errs := Apply(f.cfg, f.tree, f.mounts, tt.patch)
			if len(errs) != 1 {
				t.Errorf("errs = %v, want one", errs)
			}
			if !applied.Empty() {
				t.Errorf("applied = %+v, want nothing to forward", applied)
			}
			f.assertFile("s/Main.luau", "main")
			f.assertNoEcho()
		})
	}
}

func TestApplyContinuesAfterBadChange(t *testing.T) {
	f := newFixture(t, nil, syncW)
	applied, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		script("bad", "nope", "Bad", "Script", ""),
		script("good", f.ref("game/W"), "Good", "Script", "good"),
	}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	if len(applied.Added) != 1 || applied.Added[0].ID != "good" {
		t.Errorf("applied = %+v, want only the good one", applied)
	}
	f.assertFile("s/Good.server.luau", "good")
}

func TestApplyRejectsNonStringSource(t *testing.T) {
	f := newFixture(t, map[string]string{"s/Main.luau": "main"}, syncW)
	id := f.ref("game/W/Main")
	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Updated: []protocol.Update{{
		ID: id, Props: map[string]protocol.Value{"Source": {Type: "Number", Value: json.RawMessage("1")}},
	}}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	f.assertFile("s/Main.luau", "main")
	f.assertNoEcho()
}

func TestApplyFailedSourceWriteKeepsEdit(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only folders")
	}
	f := newFixture(t, map[string]string{"s/Main.luau": "old"}, syncW)
	id := f.ref("game/W/Main")
	dir := filepath.Join(f.cfg.Root, "s")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Updated: []protocol.Update{{ID: id, Props: srcProps("new")}}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	// The file still says "old". A scan mustn't send that back over the user's edit.
	f.assertNoEcho()

	// Later patches don't repeat the error.
	_, errs = Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Updated: []protocol.Update{{ID: id, Props: srcProps("newer")}}})
	if len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Any patch retries, and the old text still gets a copy in the trash.
	f.apply(protocol.Patch{})
	f.assertFile("s/Main.luau", "newer")
	if f.tree.IsFileless(id) {
		t.Error("it should have its file back")
	}
	if got := f.trashed("s/Main.luau"); !slices.Equal(got, []string{"old"}) {
		t.Errorf("trash = %v", got)
	}
}

func TestApplyFailedAddIsNotTakenAsDeletion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only folders")
	}
	f := newFixture(t, map[string]string{"s/.keep": ""}, syncW)
	dir := filepath.Join(f.cfg.Root, "s")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	_, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Added: []protocol.Instance{
		script("a", f.ref("game/W"), "Main", "Script", "x"),
	}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	f.assertNoEcho() // a Removed here would delete it from Studio
}

func TestApplyFailedRenameKeepsOldPlace(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to read-only folders")
	}
	f := newFixture(t, map[string]string{"s/Main.luau": "x"}, syncW)
	id := f.ref("game/W/Main")
	dir := filepath.Join(f.cfg.Root, "s")
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	applied, errs := Apply(f.cfg, f.tree, f.mounts, protocol.Patch{Updated: []protocol.Update{{ID: id, Name: "Other"}}})
	if len(errs) != 1 {
		t.Errorf("errs = %v, want one", errs)
	}
	if !applied.Empty() {
		t.Errorf("applied = %+v, want nothing to forward", applied)
	}
	if inst, _ := f.tree.Get(id); inst.Name != "Main" {
		t.Errorf("name = %s, want the old one", inst.Name)
	}
	f.assertFile("s/Main.luau", "x")
	f.assertNoEcho()
}

func TestApplyDoesntFollowSymlinkOutOfProject(t *testing.T) {
	outside := t.TempDir()
	f := newFixture(t, map[string]string{"s/.keep": ""}, syncW)
	if err := os.Symlink(outside, filepath.Join(f.cfg.Root, "s", "Evil")); err != nil {
		t.Skip(err)
	}

	// The scan doesn't follow it, so this can't resolve to the link either.
	f.apply(protocol.Patch{Added: []protocol.Instance{
		{ID: "dir", Parent: f.ref("game/W"), Name: "Evil", Class: "Folder"},
		script("kid", "dir", "Kid", "ModuleScript", "x"),
	}})
	f.assertFile("s/Evil"+tagFor("dir")+"/Kid.luau", "x")
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("wrote outside the project: %v", entries)
	}
}
