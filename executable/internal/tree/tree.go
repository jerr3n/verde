// Package tree holds Verde's in-memory copy of the instances being synced.
//
// It mirrors the shape of the game in Studio:
//
//	game
//	└── ReplicatedStorage
//	    └── lib
//	        └── Hello
//
// Everything is stored in one map, keyed by Ref, so any instance can be found
// directly without walking down from the root. Each instance also records its
// parent, and each node records its children, so the tree can be walked both ways.
// The methods here keep those two records in agreement.
//
// A Tree isn't safe for concurrent use. One goroutine should own it, and everything
// else should ask that goroutine to make changes.
package tree

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// alias for protocol.Ref
type Ref = protocol.Ref

var (
	ErrEmptyRef      = errors.New("instance has no ref")
	ErrNotFound      = errors.New("instance not found")
	ErrDuplicateRef  = errors.New("ref already exists")
	ErrRootImmutable = errors.New("cannot remove, rename or move the root")
	ErrCycle         = errors.New("cannot move an instance into itself")
)

// one instance + children's refs
type node struct {
	inst     protocol.Instance
	children []Ref
	fileless bool // should have a file but doesn't, so disk says nothing about it
}

// Tree is the set of synced instances. Create one with New.
type Tree struct {
	root  Ref
	nodes map[Ref]*node
}

// creates  a tree containing only the root
func New(root protocol.Instance) *Tree {
	root = clone(root)
	root.Parent = "" // The root is the one instance with no parent.
	return &Tree{
		root:  root.ID,
		nodes: map[Ref]*node{root.ID: {inst: root}},
	}
}

// returns root
func (t *Tree) Root() Ref { return t.root }

// number of instances
func (t *Tree) Len() int { return len(t.nodes) }

// finds one instance by its ref
func (t *Tree) Get(id Ref) (protocol.Instance, bool) {
	n, ok := t.nodes[id]
	if !ok {
		return protocol.Instance{}, false
	}
	return clone(n.inst), true
}

// lists an instance's children, in order
func (t *Tree) Children(id Ref) []Ref {
	n, ok := t.nodes[id]
	if !ok {
		return nil
	}
	return slices.Clone(n.children)
}

// SetFileless records whether an instance is missing a file it would normally have, such
// as a second Script with the same name, or one whose write failed. Diff leaves these
// alone: not finding the file doesn't mean it was deleted, and an older file with the
// same name doesn't mean its contents changed.
func (t *Tree) SetFileless(id Ref, fileless bool) {
	if n, ok := t.nodes[id]; ok {
		n.fileless = fileless
	}
}

// reports whether an instance is marked fileless
func (t *Tree) IsFileless(id Ref) bool {
	n, ok := t.nodes[id]
	return ok && n.fileless
}

// lists every fileless instance, parents first
func (t *Tree) Fileless() []Ref {
	var out []Ref
	var visit func(id Ref)
	visit = func(id Ref) {
		n := t.nodes[id]
		if n.fileless {
			out = append(out, id)
		}
		for _, child := range n.children {
			visit(child)
		}
	}
	visit(t.root)
	return out
}

// adds instance as last child of its parent
func (t *Tree) Insert(inst protocol.Instance) error {
	if inst.ID == "" {
		return ErrEmptyRef
	}
	if _, exists := t.nodes[inst.ID]; exists {
		return fmt.Errorf("insert %s: %w", inst.ID, ErrDuplicateRef)
	}
	parent, ok := t.nodes[inst.Parent]
	if !ok {
		return fmt.Errorf("insert %s: parent %s: %w", inst.ID, inst.Parent, ErrNotFound)
	}

	t.nodes[inst.ID] = &node{inst: clone(inst)}
	parent.children = append(parent.children, inst.ID)
	return nil
}

// renames, moves, merges properties
func (t *Tree) Update(u protocol.Update) error {
	n, ok := t.nodes[u.ID]
	if !ok {
		return fmt.Errorf("update %s: %w", u.ID, ErrNotFound)
	}
	if u.ID == t.root && u.Name != "" {
		return fmt.Errorf("update %s: %w", u.ID, ErrRootImmutable)
	}

	// Moving is the only step left that can fail, so it goes first. That way a
	// failed move can't leave a rename half applied.
	if u.Parent != "" {
		if err := t.move(n, u.Parent); err != nil {
			return fmt.Errorf("update %s: %w", u.ID, err)
		}
	}
	if u.Name != "" {
		n.inst.Name = u.Name
	}

	// Merge rather than replace, because an update carries only what changed. A Nil
	// value is stored like any other. Deleting it instead would drop it from later
	// snapshots, and the plugin leaves properties missing from a snapshot untouched,
	// so Studio could keep the old value after a reconnect.
	if len(u.Props) > 0 && n.inst.Props == nil {
		n.inst.Props = make(map[string]protocol.Value, len(u.Props))
	}
	maps.Copy(n.inst.Props, u.Props)
	return nil
}

// deos move for update
func (t *Tree) move(n *node, newParent Ref) error {
	id := n.inst.ID
	if id == t.root {
		return ErrRootImmutable
	}
	target, ok := t.nodes[newParent]
	if !ok {
		return fmt.Errorf("parent %s: %w", newParent, ErrNotFound)
	}
	if n.inst.Parent == newParent {
		return nil
	}

	// Walk up from the new parent to the root. Finding n on the way means n would
	// end up inside itself, cutting that whole branch off from the root.
	for ancestor := newParent; ancestor != ""; ancestor = t.nodes[ancestor].inst.Parent {
		if ancestor == id {
			return ErrCycle
		}
	}

	oldParent := t.nodes[n.inst.Parent]
	oldParent.children = removeRef(oldParent.children, id)
	target.children = append(target.children, id)
	n.inst.Parent = newParent
	return nil
}

// deletes instance and everything underneath it
func (t *Tree) Remove(id Ref) error {
	if id == t.root {
		return ErrRootImmutable
	}
	n, ok := t.nodes[id]
	if !ok {
		return nil
	}

	parent := t.nodes[n.inst.Parent]
	parent.children = removeRef(parent.children, id)

	pending := []Ref{id}
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]

		pending = append(pending, t.nodes[current].children...)
		delete(t.nodes, current)
	}
	return nil
}

// applies a patch in protocol order: added, updated, then removed. It stops at the
// first error, and anything before that stays applied
func (t *Tree) Apply(p protocol.Patch) error {
	for _, inst := range p.Added {
		if err := t.Insert(inst); err != nil {
			return err
		}
	}
	for _, u := range p.Updated {
		if err := t.Update(u); err != nil {
			return err
		}
	}
	for _, id := range p.Removed {
		if err := t.Remove(id); err != nil {
			return err
		}
	}
	return nil
}

// lists copies of every instance
func (t *Tree) All() []protocol.Instance {
	out := make([]protocol.Instance, 0, len(t.nodes))

	var visit func(id Ref)
	visit = func(id Ref) {
		n := t.nodes[id]
		out = append(out, clone(n.inst))
		for _, child := range n.children {
			visit(child)
		}
	}
	visit(t.root)

	return out
}

// appends copies of an instance and everything under it to out, parents first. The
// top instance gets the given parent, since its real one may be in another tree
func (t *Tree) appendSubtree(out []protocol.Instance, id, parent Ref) []protocol.Instance {
	n := t.nodes[id]
	inst := clone(n.inst)
	inst.Parent = parent
	out = append(out, inst)
	for _, child := range n.children {
		out = t.appendSubtree(out, child, id)
	}
	return out
}

// removes one ref from a slice
func removeRef(refs []Ref, id Ref) []Ref {
	return slices.DeleteFunc(refs, func(r Ref) bool { return r == id })
}

// copies instance + its propsmap
func clone(inst protocol.Instance) protocol.Instance {
	inst.Props = maps.Clone(inst.Props)
	return inst
}
