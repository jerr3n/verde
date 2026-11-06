package tree

import (
	"reflect"
	"slices"
	"testing"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

func source(s string) map[string]protocol.Value {
	return map[string]protocol.Value{"Source": str(s)}
}

// buildTree makes a tree with the given root Ref. Scans get new Refs every time, so
// tests give the current tree and the scan different ones.
func buildTree(t *testing.T, root Ref, insts ...protocol.Instance) *Tree {
	t.Helper()
	tr := New(protocol.Instance{ID: root, Name: "game", Class: "DataModel"})
	for _, inst := range insts {
		if err := tr.Insert(inst); err != nil {
			t.Fatalf("Insert(%s): %v", inst.ID, err)
		}
	}
	return tr
}

// shape describes a tree without its Refs, so trees from different scans can be compared.
func shape(tr *Tree) []string {
	paths := map[Ref]string{}
	var out []string
	for _, inst := range tr.All() {
		paths[inst.ID] = paths[inst.Parent] + "/" + inst.Name
		out = append(out, paths[inst.ID]+" "+inst.Class+" "+string(inst.Props["Source"].Value))
	}
	slices.Sort(out)
	return out
}

func assertPatch(t *testing.T, got, want protocol.Patch) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("patch = %+v, want %+v", got, want)
	}
}

func TestDiffNoChanges(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "lib", Parent: "rs", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util", Parent: "lib", Name: "Util", Class: "ModuleScript", Props: source("a")},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "lib2", Parent: "rs2", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util2", Parent: "lib2", Name: "Util", Class: "ModuleScript", Props: source("a")},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{})
}

func TestDiffSourceChangedUsesCurrentRef(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "lib", Parent: "rs", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util", Parent: "lib", Name: "Util", Class: "ModuleScript", Props: source("old")},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "lib2", Parent: "rs2", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util2", Parent: "lib2", Name: "Util", Class: "ModuleScript", Props: source("new")},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{
		Updated: []protocol.Update{{ID: "util", Props: source("new")}},
	})
}

func TestDiffNewScriptInNewFolder(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "lib2", Parent: "rs2", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util2", Parent: "lib2", Name: "Util", Class: "ModuleScript", Props: source("a")},
	)

	// The folder comes first and points at the Ref the plugin already knows. The script
	// points at the folder, which is new in the same patch.
	assertPatch(t, Diff(cur, next), protocol.Patch{
		Added: []protocol.Instance{
			{ID: "lib2", Parent: "rs", Name: "lib", Class: "Folder"},
			{ID: "util2", Parent: "lib2", Name: "Util", Class: "ModuleScript", Props: source("a")},
		},
	})
}

func TestDiffScriptRemoved(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "util", Parent: "rs", Name: "Util", Class: "ModuleScript", Props: source("a")},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{Removed: []Ref{"util"}})
}

// Removing a Ref removes its subtree, so only the top of a removed folder is listed.
func TestDiffFolderRemovedListsOnlyTop(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "lib", Parent: "rs", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util", Parent: "lib", Name: "Util", Class: "ModuleScript", Props: source("a")},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{Removed: []Ref{"lib"}})
}

func TestDiffClassChangeReplacesInstance(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "foo", Parent: "rs", Name: "Foo", Class: "ModuleScript", Props: source("a")},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "foo2", Parent: "rs2", Name: "Foo", Class: "Script", Props: source("a")},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{
		Added:   []protocol.Instance{{ID: "foo2", Parent: "rs", Name: "Foo", Class: "Script", Props: source("a")}},
		Removed: []Ref{"foo"},
	})
}

// A scan can't see a Part from Studio, so not finding one doesn't mean it was deleted.
func TestDiffKeepsClassesNotOnDisk(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "part", Parent: "rs", Name: "Lamp", Class: "Part"},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{})
}

// A scan can't see tags or attributes either, so they aren't removed or sent.
func TestDiffKeepsPropsNotOnDisk(t *testing.T) {
	props := source("a")
	props["$tags"] = protocol.Value{Type: "Tags", Value: []byte(`["TagA"]`)}
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "foo", Parent: "rs", Name: "Foo", Class: "Script", Props: props},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "foo2", Parent: "rs2", Name: "Foo", Class: "Script", Props: source("a")},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{})
}

// Studio's JSON may escape text differently from Go's. That isn't a real change.
func TestDiffIgnoresStringEscaping(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "foo", Parent: "rs", Name: "Foo", Class: "Script", Props: map[string]protocol.Value{
			"Source": {Type: "String", Value: []byte(`"café"`)},
		}},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "foo2", Parent: "rs2", Name: "Foo", Class: "Script", Props: map[string]protocol.Value{
			"Source": {Type: "String", Value: []byte(`"café"`)},
		}},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{})
}

// Applying a patch should leave the tree matching the scan, with nothing left to send.
func TestDiffApplyConverges(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "edited", Parent: "rs", Name: "Edited", Class: "ModuleScript", Props: source("old")},
		protocol.Instance{ID: "gone", Parent: "rs", Name: "Gone", Class: "Folder"},
		protocol.Instance{ID: "inner", Parent: "gone", Name: "Inner", Class: "Script", Props: source("a")},
		protocol.Instance{ID: "foo", Parent: "rs", Name: "Foo", Class: "ModuleScript", Props: source("a")},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "edited2", Parent: "rs2", Name: "Edited", Class: "ModuleScript", Props: source("new")},
		protocol.Instance{ID: "lib2", Parent: "rs2", Name: "lib", Class: "Folder"},
		protocol.Instance{ID: "util2", Parent: "lib2", Name: "Util", Class: "ModuleScript", Props: source("a")},
		protocol.Instance{ID: "foo2", Parent: "rs2", Name: "Foo", Class: "Script", Props: source("a")},
	)

	if err := cur.Apply(Diff(cur, next)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got, want := shape(cur), shape(next); !slices.Equal(got, want) {
		t.Errorf("shape = %v, want %v", got, want)
	}
	assertPatch(t, Diff(cur, next), protocol.Patch{})
}
