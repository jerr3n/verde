// SPDX-License-Identifier: MPL-2.0

package tree

import (
	"errors"
	"slices"
	"testing"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// newTestTree builds this tree:
//
//	game
//	└── rs
//	    ├── a
//	    │   └── b
//	    │       └── s
//	    └── c
func newTestTree(t *testing.T) *Tree {
	t.Helper()
	tr := New(protocol.Instance{ID: "game", Name: "game", Class: "DataModel"})
	for _, inst := range []protocol.Instance{
		{ID: "rs", Parent: "game", Name: "ReplicatedStorage", Class: "ReplicatedStorage"},
		{ID: "a", Parent: "rs", Name: "A", Class: "Folder"},
		{ID: "b", Parent: "a", Name: "B", Class: "Folder"},
		{ID: "s", Parent: "b", Name: "S", Class: "Script"},
		{ID: "c", Parent: "rs", Name: "C", Class: "Folder"},
	} {
		if err := tr.Insert(inst); err != nil {
			t.Fatalf("Insert(%s): %v", inst.ID, err)
		}
	}
	return tr
}

// refs lists the tree's Refs in the order All returns them.
func refs(tr *Tree) []Ref {
	var out []Ref
	for _, inst := range tr.All() {
		out = append(out, inst.ID)
	}
	return out
}

func assertRefs(t *testing.T, tr *Tree, want ...Ref) {
	t.Helper()
	if got := refs(tr); !slices.Equal(got, want) {
		t.Errorf("refs = %v, want %v", got, want)
	}
}

func str(s string) protocol.Value {
	return protocol.Value{Type: "String", Value: []byte(`"` + s + `"`)}
}

func TestNew(t *testing.T) {
	props := map[string]protocol.Value{"A": str("1")}
	tr := New(protocol.Instance{ID: "game", Parent: "ignored", Name: "game", Class: "DataModel", Props: props})

	if tr.Root() != "game" || tr.Len() != 1 {
		t.Fatalf("Root = %q, Len = %d", tr.Root(), tr.Len())
	}
	root, _ := tr.Get("game")
	if root.Parent != "" {
		t.Errorf("root.Parent = %q, want empty", root.Parent)
	}

	props["A"] = str("changed")
	root, _ = tr.Get("game")
	if string(root.Props["A"].Value) != `"1"` {
		t.Errorf("New kept a reference to the caller's map: %s", root.Props["A"].Value)
	}
}

func TestGetUnknown(t *testing.T) {
	if inst, ok := newTestTree(t).Get("nope"); ok {
		t.Errorf("Get returned %+v for an unknown Ref", inst)
	}
}

func TestInsertErrors(t *testing.T) {
	tests := []struct {
		name string
		inst protocol.Instance
		want error
	}{
		{"empty ref", protocol.Instance{Parent: "rs"}, ErrEmptyRef},
		{"duplicate ref", protocol.Instance{ID: "a", Parent: "c"}, ErrDuplicateRef},
		{"unknown parent", protocol.Instance{ID: "x", Parent: "nope"}, ErrNotFound},
		{"no parent", protocol.Instance{ID: "x"}, ErrNotFound},
		{"own parent", protocol.Instance{ID: "x", Parent: "x"}, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newTestTree(t)
			before := refs(tr)
			if err := tr.Insert(tt.inst); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			assertRefs(t, tr, before...)
		})
	}
}

func TestAllListsParentsFirst(t *testing.T) {
	assertRefs(t, newTestTree(t), "game", "rs", "a", "b", "s", "c")
}

func TestSiblingOrderIsStable(t *testing.T) {
	tr := New(protocol.Instance{ID: "game"})
	for _, id := range []Ref{"3", "1", "2"} {
		if err := tr.Insert(protocol.Instance{ID: id, Parent: "game", Name: "Part", Class: "Part"}); err != nil {
			t.Fatal(err)
		}
	}
	for range 20 {
		assertRefs(t, tr, "game", "3", "1", "2")
	}
}

func TestUpdateRenames(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Update(protocol.Update{ID: "a", Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	inst, _ := tr.Get("a")
	if inst.Name != "Renamed" {
		t.Errorf("Name = %q", inst.Name)
	}
}

func TestUpdateWithEmptyFieldsChangesNothing(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Update(protocol.Update{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	inst, _ := tr.Get("a")
	if inst.Name != "A" || inst.Parent != "rs" {
		t.Errorf("instance changed: %+v", inst)
	}
}

func TestUpdateMergesPropsAndKeepsNil(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Update(protocol.Update{ID: "s", Props: map[string]protocol.Value{
		"Source":   str("print(1)"),
		"Disabled": str("x"),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Update(protocol.Update{ID: "s", Props: map[string]protocol.Value{
		"Disabled": {Type: "Nil"},
	}}); err != nil {
		t.Fatal(err)
	}

	inst, _ := tr.Get("s")
	if inst.Props["Source"].Type != "String" {
		t.Errorf("Source was lost by the merge: %+v", inst.Props)
	}
	if got, ok := inst.Props["Disabled"]; !ok || got.Type != "Nil" {
		t.Errorf("Disabled = %+v, want it stored as Nil", got)
	}
}

func TestUpdateUnknownRef(t *testing.T) {
	if err := newTestTree(t).Update(protocol.Update{ID: "nope"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestMove(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Update(protocol.Update{ID: "b", Parent: "c"}); err != nil {
		t.Fatal(err)
	}
	// The whole subtree moves, and a is left with no children.
	assertRefs(t, tr, "game", "rs", "a", "c", "b", "s")

	inst, _ := tr.Get("b")
	if inst.Parent != "c" {
		t.Errorf("Parent = %q, want c", inst.Parent)
	}
}

func TestMoveToSameParentKeepsOrder(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Update(protocol.Update{ID: "a", Parent: "rs"}); err != nil {
		t.Fatal(err)
	}
	assertRefs(t, tr, "game", "rs", "a", "b", "s", "c")
}

func TestFailedUpdateChangesNothing(t *testing.T) {
	tests := []struct {
		name   string
		update protocol.Update
		want   error
	}{
		{"into itself", protocol.Update{ID: "a", Parent: "a", Name: "New"}, ErrCycle},
		{"into its child", protocol.Update{ID: "a", Parent: "b", Name: "New"}, ErrCycle},
		{"into a deep descendant", protocol.Update{ID: "a", Parent: "s", Name: "New"}, ErrCycle},
		{"unknown parent", protocol.Update{ID: "a", Parent: "nope", Name: "New"}, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newTestTree(t)
			before := refs(tr)
			if err := tr.Update(tt.update); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			inst, _ := tr.Get("a")
			if inst.Name != "A" || inst.Parent != "rs" {
				t.Errorf("instance changed: %+v", inst)
			}
			assertRefs(t, tr, before...)
		})
	}
}

func TestRootIsImmutable(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Tree) error
	}{
		{"remove", func(tr *Tree) error { return tr.Remove("game") }},
		{"rename", func(tr *Tree) error { return tr.Update(protocol.Update{ID: "game", Name: "New"}) }},
		{"move", func(tr *Tree) error { return tr.Update(protocol.Update{ID: "game", Parent: "rs"}) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newTestTree(t)
			before := refs(tr)
			if err := tt.run(tr); !errors.Is(err, ErrRootImmutable) {
				t.Fatalf("err = %v, want ErrRootImmutable", err)
			}
			assertRefs(t, tr, before...)
		})
	}
}

func TestRootPropsCanChange(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Update(protocol.Update{ID: "game", Props: map[string]protocol.Value{"A": str("1")}}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveDeletesSubtree(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Remove("a"); err != nil {
		t.Fatal(err)
	}
	assertRefs(t, tr, "game", "rs", "c")
	if tr.Len() != 3 {
		t.Errorf("Len = %d, want 3", tr.Len())
	}
	for _, id := range []Ref{"a", "b", "s"} {
		if _, ok := tr.Get(id); ok {
			t.Errorf("%s still exists", id)
		}
	}
}

// The plugin can list a child after its parent, or the same Ref twice.
func TestRemoveUnknownIsNoOp(t *testing.T) {
	tr := newTestTree(t)
	for _, id := range []Ref{"a", "a", "s", "never-existed"} {
		if err := tr.Remove(id); err != nil {
			t.Fatalf("Remove(%s): %v", id, err)
		}
	}
	assertRefs(t, tr, "game", "rs", "c")
}

func TestRemoveChildThenParent(t *testing.T) {
	tr := newTestTree(t)
	for _, id := range []Ref{"s", "b", "a"} {
		if err := tr.Remove(id); err != nil {
			t.Fatalf("Remove(%s): %v", id, err)
		}
	}
	assertRefs(t, tr, "game", "rs", "c")
}

// Removing an instance frees its Ref, so the same Ref can be inserted again.
func TestInsertAfterRemove(t *testing.T) {
	tr := newTestTree(t)
	if err := tr.Remove("c"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Insert(protocol.Instance{ID: "c", Parent: "a", Name: "C", Class: "Folder"}); err != nil {
		t.Fatal(err)
	}
	assertRefs(t, tr, "game", "rs", "a", "b", "s", "c")
}

func TestCopiesDontShareProps(t *testing.T) {
	tr := newTestTree(t)
	original := map[string]protocol.Value{"Source": str("original")}
	if err := tr.Insert(protocol.Instance{ID: "x", Parent: "rs", Props: original}); err != nil {
		t.Fatal(err)
	}

	assertUnchanged := func(t *testing.T) {
		t.Helper()
		inst, _ := tr.Get("x")
		if string(inst.Props["Source"].Value) != `"original"` {
			t.Errorf("Source = %s, want it unchanged", inst.Props["Source"].Value)
		}
	}

	t.Run("map passed to Insert", func(t *testing.T) {
		original["Source"] = str("changed")
		assertUnchanged(t)
	})
	t.Run("result of Get", func(t *testing.T) {
		inst, _ := tr.Get("x")
		inst.Props["Source"] = str("changed")
		assertUnchanged(t)
	})
	t.Run("result of All", func(t *testing.T) {
		for _, inst := range tr.All() {
			if inst.ID == "x" {
				inst.Props["Source"] = str("changed")
			}
		}
		assertUnchanged(t)
	})
}
