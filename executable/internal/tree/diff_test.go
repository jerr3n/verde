// SPDX-License-Identifier: MPL-2.0

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

// every class has a file now, so not finding a part means it was deleted
func TestDiffRemovesAnyClassGoneFromDisk(t *testing.T) {
	cur := buildTree(t, "game",
		protocol.Instance{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		protocol.Instance{ID: "part", Parent: "rs", Name: "Lamp", Class: "Part"},
	)
	next := buildTree(t, "game2",
		protocol.Instance{ID: "rs2", Parent: "game2", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
	)

	assertPatch(t, Diff(cur, next), protocol.Patch{Removed: []Ref{"part"}})
}

// a scan can't see refs, so they aren't removed or sent
func TestDiffKeepsPropsNotOnDisk(t *testing.T) {
	props := source("a")
	props["PrimaryPart"] = protocol.Value{Type: "Ref", Value: []byte(`"some-ref"`)}
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

// a file can hold the same value in different json. treating that as a change would send
// an update to studio on every scan
func TestDiffIgnoresValueFormatting(t *testing.T) {
	sameValues := []struct{ a, b string }{
		{`[1,2,3]`, `[1, 2, 3]`},
		{`[1.0,2]`, `[1,2.0]`},
		{`["a<b",{"t":"Number","v":1}]`, `["a<b", {"v":1,"t":"Number"}]`},
	}
	for _, tc := range sameValues {
		a := protocol.Value{Type: "Vector3", Value: []byte(tc.a)}
		b := protocol.Value{Type: "Vector3", Value: []byte(tc.b)}
		if !sameValue(a, b) {
			t.Errorf("%s and %s should be the same", tc.a, tc.b)
		}
	}

	a := protocol.Value{Type: "Vector3", Value: []byte(`[1,2,3]`)}
	if sameValue(a, protocol.Value{Type: "Vector3", Value: []byte(`[1,2,4]`)}) {
		t.Error("different numbers should differ")
	}
	if sameValue(a, protocol.Value{Type: "Color3", Value: []byte(`[1,2,3]`)}) {
		t.Error("different types should differ")
	}
	if !sameValue(protocol.Value{Type: "Nil"}, protocol.Value{Type: "Nil"}) {
		t.Error("Nil has no value, and should equal itself")
	}
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

func TestDiffLeavesFilelessInstanceAlone(t *testing.T) {
	build := func(root Ref) *Tree {
		return buildTree(t, root,
			protocol.Instance{ID: root + "rs", Parent: root, Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		)
	}

	t.Run("missing from disk isn't a deletion", func(t *testing.T) {
		cur := build("game")
		cur.Insert(protocol.Instance{ID: "a", Parent: "gamers", Name: "A", Class: "ModuleScript", Props: source("x")})
		cur.SetFileless("a", true)

		assertPatch(t, Diff(cur, build("game2")), protocol.Patch{})
	})

	t.Run("a file with its name isn't its file", func(t *testing.T) {
		cur := build("game")
		cur.Insert(protocol.Instance{ID: "a", Parent: "gamers", Name: "A", Class: "ModuleScript", Props: source("mine")})
		cur.SetFileless("a", true)
		next := build("game2")
		next.Insert(protocol.Instance{ID: "a2", Parent: "game2rs", Name: "A", Class: "ModuleScript", Props: source("older")})

		assertPatch(t, Diff(cur, next), protocol.Patch{})
	})

	t.Run("its children aren't deleted either", func(t *testing.T) {
		cur := build("game")
		cur.Insert(protocol.Instance{ID: "dir", Parent: "gamers", Name: "dir", Class: "Folder"})
		cur.Insert(protocol.Instance{ID: "kid", Parent: "dir", Name: "Kid", Class: "ModuleScript", Props: source("x")})
		cur.SetFileless("dir", true)
		next := build("game2")
		next.Insert(protocol.Instance{ID: "dir2", Parent: "game2rs", Name: "dir", Class: "Folder"})

		assertPatch(t, Diff(cur, next), protocol.Patch{})
	})
}

func TestDiffSameNameSiblingsMatchTheOneWithTheFile(t *testing.T) {
	for _, order := range [][]Ref{{"withFile", "without"}, {"without", "withFile"}} {
		cur := buildTree(t, "game", protocol.Instance{ID: "rs", Parent: "game", Name: "R", Class: "R"})
		for _, id := range order {
			cur.Insert(protocol.Instance{ID: id, Parent: "rs", Name: "Dup", Class: "ModuleScript", Props: source(string(id))})
		}
		cur.SetFileless("without", true)

		next := buildTree(t, "game2", protocol.Instance{ID: "rs2", Parent: "game2", Name: "R", Class: "R"})
		next.Insert(protocol.Instance{ID: "dup2", Parent: "rs2", Name: "Dup", Class: "ModuleScript", Props: source("edited")})

		assertPatch(t, Diff(cur, next), protocol.Patch{
			Updated: []protocol.Update{{ID: "withFile", Props: source("edited")}},
		})
	}
}

func TestDiffStoredRefs(t *testing.T) {
	rs := protocol.Instance{ID: "rs", Parent: "game", Name: "R", Class: "R"}
	rs2 := protocol.Instance{ID: "rs2", Parent: "game2", Name: "R", Class: "R"}
	part := func(id, parent Ref, name string) protocol.Instance {
		return protocol.Instance{ID: id, Parent: parent, Name: name, Class: "Part"}
	}

	t.Run("same-named siblings match by Ref, not order", func(t *testing.T) {
		cur := buildTree(t, "game", rs, part("a", "rs", "Part"), part("b", "rs", "Part"))
		next := buildTree(t, "game2", rs2, part("b", "rs2", "Part"), part("a", "rs2", "Part"))
		next.Update(protocol.Update{ID: "b", Props: source("edited")})

		assertPatch(t, Diff(cur, next), protocol.Patch{
			Updated: []protocol.Update{{ID: "b", Props: source("edited")}},
		})
	})

	t.Run("a stored Ref and one with no id", func(t *testing.T) {
		cur := buildTree(t, "game", rs, part("a", "rs", "Part"), part("b", "rs", "Part"))
		next := buildTree(t, "game2", rs2, part("random", "rs2", "Part"), part("b", "rs2", "Part"))

		assertPatch(t, Diff(cur, next), protocol.Patch{})
	})

	t.Run("gone from disk is removed", func(t *testing.T) {
		cur := buildTree(t, "game", rs, part("a", "rs", "Part"), part("b", "rs", "Part"))
		next := buildTree(t, "game2", rs2, part("a", "rs2", "Part"))

		assertPatch(t, Diff(cur, next), protocol.Patch{Removed: []Ref{"b"}})
	})

	t.Run("a moved file moves the instance", func(t *testing.T) {
		folder := func(id, parent Ref) protocol.Instance {
			return protocol.Instance{ID: id, Parent: parent, Name: "lib", Class: "Folder"}
		}
		cur := buildTree(t, "game", rs, folder("dir", "rs"), part("a", "dir", "Part"))
		next := buildTree(t, "game2", rs2, folder("dir2", "rs2"), part("a", "rs2", "Tire"))

		assertPatch(t, Diff(cur, next), protocol.Patch{
			Updated: []protocol.Update{{ID: "a", Parent: "rs", Name: "Tire"}},
		})
	})

	t.Run("moving into a new folder adds the folder first", func(t *testing.T) {
		cur := buildTree(t, "game", rs, part("a", "rs", "Part"))
		next := buildTree(t, "game2", rs2,
			protocol.Instance{ID: "new", Parent: "rs2", Name: "lib", Class: "Folder"},
			part("a", "new", "Part"),
		)

		assertPatch(t, Diff(cur, next), protocol.Patch{
			Added:   []protocol.Instance{{ID: "new", Parent: "rs", Name: "lib", Class: "Folder"}},
			Updated: []protocol.Update{{ID: "a", Parent: "new"}},
		})
	})

	t.Run("moving out of a removed folder keeps the instance", func(t *testing.T) {
		cur := buildTree(t, "game", rs,
			protocol.Instance{ID: "dir", Parent: "rs", Name: "lib", Class: "Folder"},
			part("a", "dir", "Part"),
		)
		next := buildTree(t, "game2", rs2, part("a", "rs2", "Part"))

		patch := Diff(cur, next)
		assertPatch(t, patch, protocol.Patch{
			Updated: []protocol.Update{{ID: "a", Parent: "rs"}},
			Removed: []Ref{"dir"},
		})
		if err := cur.Apply(patch); err != nil {
			t.Fatal(err)
		}
		if _, ok := cur.Get("a"); !ok {
			t.Error("a should still exist")
		}
	})

	t.Run("swapping parents can't make a cycle", func(t *testing.T) {
		cur := buildTree(t, "game", rs, part("a", "rs", "A"), part("b", "a", "B"))
		next := buildTree(t, "game2", rs2, part("b", "rs2", "B"), part("a", "b", "A"))

		if err := cur.Apply(Diff(cur, next)); err != nil {
			t.Fatal(err)
		}
		assertPatch(t, Diff(cur, next), protocol.Patch{})
	})

	t.Run("a changed class can't reuse the Ref", func(t *testing.T) {
		cur := buildTree(t, "game", rs, part("a", "rs", "Lamp"))
		next := buildTree(t, "game2", rs2, protocol.Instance{ID: "a", Parent: "rs2", Name: "Lamp", Class: "Model"})

		patch := Diff(cur, next)
		if len(patch.Added) != 1 || patch.Added[0].ID == "a" || len(patch.Removed) != 1 || patch.Removed[0] != "a" {
			t.Fatalf("patch = %+v", patch)
		}
		if err := cur.Apply(patch); err != nil {
			t.Fatal(err)
		}
		assertPatch(t, Diff(cur, next), protocol.Patch{})
	})
}
