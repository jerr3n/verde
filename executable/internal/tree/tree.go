// SPDX-License-Identifier: MPL-2.0

// Package tree holds the server's copy of the synced instances, and finds the changes between two copies.
package tree

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// Ref is the same type as protocol.Ref.
type Ref = protocol.Ref

// These errors tell why the Tree did not apply a change.
var (
	ErrEmptyRef      = errors.New("instance has no ref")
	ErrNotFound      = errors.New("instance not found")
	ErrDuplicateRef  = errors.New("ref already exists")
	ErrRootImmutable = errors.New("cannot remove, rename or move the root")
	ErrCycle         = errors.New("cannot move an instance into itself")
)

// node is one instance and the refs of its children, in order.
type node struct {
	inst     protocol.Instance
	children []Ref
	// fileless is true when the instance must have a file on disk but has no file.
	// See SetFileless.
	fileless bool
}

// Tree is the set of synced instances. Each instance has a unique Ref.
// Make a Tree with New. A Tree is not safe for use by more than one goroutine at the same time.
type Tree struct {
	root  Ref
	nodes map[Ref]*node
}

// New makes a tree that holds only root. New clears root.Parent.
func New(root protocol.Instance) *Tree {
	root = clone(root)
	// The root is the only instance with no parent.
	root.Parent = ""
	return &Tree{
		root:  root.ID,
		nodes: map[Ref]*node{root.ID: {inst: root}},
	}
}

// Root returns the ref of the root instance.
func (t *Tree) Root() Ref { return t.root }

// Len returns the number of instances, including the root.
func (t *Tree) Len() int { return len(t.nodes) }

// Get returns a copy of the instance with the ref id. The bool is false if the tree has no such instance.
func (t *Tree) Get(id Ref) (protocol.Instance, bool) {
	n, ok := t.nodes[id]
	if !ok {
		return protocol.Instance{}, false
	}
	return clone(n.inst), true
}

// Children returns the refs of the children of id, in order. It returns nil if id is not in the tree.
func (t *Tree) Children(id Ref) []Ref {
	n, ok := t.nodes[id]
	if !ok {
		return nil
	}
	return slices.Clone(n.children)
}

// SetFileless records whether an instance has no file on disk where it must have one.
// Two examples are an instance whose path matches ignore.paths, and an instance whose file write failed.
// Diff does not change or remove a fileless instance, for these reasons:
//   - A missing file does not show that a person deleted the instance.
//   - An older file with the same name does not show that the instance changed.
func (t *Tree) SetFileless(id Ref, fileless bool) {
	if n, ok := t.nodes[id]; ok {
		n.fileless = fileless
	}
}

// IsFileless reports whether id is in the tree and is fileless.
func (t *Tree) IsFileless(id Ref) bool {
	n, ok := t.nodes[id]
	return ok && n.fileless
}

// Fileless returns the ref of each fileless instance. A parent comes before its children.
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

// Insert adds a copy of inst as the last child of inst.Parent. Insert returns an error in these cases:
//   - inst has no ref.
//   - The ref is already in the tree.
//   - The parent is not in the tree.
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

// Update applies u to the instance with the ref u.ID.
// A non-empty Parent moves the instance. A non-empty Name renames it.
// Each property in u.Props replaces the property with the same name. Other properties stay the same.
// If the move fails, Update changes nothing.
func (t *Tree) Update(u protocol.Update) error {
	n, ok := t.nodes[u.ID]
	if !ok {
		return fmt.Errorf("update %s: %w", u.ID, ErrNotFound)
	}
	if u.ID == t.root && u.Name != "" {
		return fmt.Errorf("update %s: %w", u.ID, ErrRootImmutable)
	}

	// The move is the only step after this point that can fail. Thus the move occurs first,
	// and a failed move cannot leave the rename applied without the move.
	if u.Parent != "" {
		if err := t.move(n, u.Parent); err != nil {
			return fmt.Errorf("update %s: %w", u.ID, err)
		}
	}
	if u.Name != "" {
		n.inst.Name = u.Name
	}

	// Merge the properties, because an update holds only the properties that changed.
	// Keep a Nil value in the map. Do not delete the property.
	// If the property is deleted, it is not in the next snapshot.
	// The plugin does not change a property that is not in a snapshot.
	// Thus Studio could keep the old value after a reconnect.
	if len(u.Props) > 0 && n.inst.Props == nil {
		n.inst.Props = make(map[string]protocol.Value, len(u.Props))
	}
	maps.Copy(n.inst.Props, u.Props)
	return nil
}

// move makes newParent the parent of n, and makes n the last child of newParent.
// move returns ErrCycle if newParent is n or a descendant of n.
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

	// Go up from the new parent to the root. If n is on that path, the move puts n inside itself.
	// That move disconnects n and its descendants from the root.
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

// Remove deletes the instance id and all of its descendants.
// Remove returns nil if id is not in the tree. Remove returns ErrRootImmutable for the root.
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

// Apply applies p in this order: p.Added, then p.Updated, then p.Removed.
// Apply stops at the first error. The changes before the error stay applied.
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

// All returns a copy of each instance. A parent comes before its children, and siblings stay in order.
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

// removeRef removes each copy of id from refs.
func removeRef(refs []Ref, id Ref) []Ref {
	return slices.DeleteFunc(refs, func(r Ref) bool { return r == id })
}

// clone copies inst and its Props map, so that the copy and the original do not share the map.
func clone(inst protocol.Instance) protocol.Instance {
	inst.Props = maps.Clone(inst.Props)
	return inst
}
