// SPDX-License-Identifier: MPL-2.0

package tree

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// sibling is the key that matches an instance without a stored ref to an instance with the same parent.
type sibling struct {
	name, class string
}

// changedProps returns each property in next that is not in cur or that has a different value in cur.
// It does not report a property that is only in cur, for two reasons:
//   - A scan of the disk does not find properties of type Ref.
//   - Studio does not change a property that is not in a patch.
func changedProps(cur, next map[string]protocol.Value) map[string]protocol.Value {
	changed := map[string]protocol.Value{}
	for name, value := range next {
		old, exists := cur[name]
		if !exists || !sameValue(old, value) {
			changed[name] = value
		}
	}
	return changed
}

// sameValue reports whether a and b have the same type and the same decoded JSON value.
func sameValue(a, b protocol.Value) bool {
	if a.Type != b.Type {
		return false
	}
	if bytes.Equal(a.Value, b.Value) {
		return true
	}
	// Compare the decoded values. Studio, Go, and a person who edits a file can write
	// the same value in different text, for example with other escapes, 1.0 for 1, or extra spaces.
	var x, y any
	if json.Unmarshal(a.Value, &x) != nil || json.Unmarshal(b.Value, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// Diff returns the patch that changes cur into next. The patch uses the refs from cur.
//
// Each scan gives new refs, except to an instance whose file stores its ref. Thus Diff matches instances in this order:
//  1. By ref, if cur has the ref with the same class. If the parent is different, the file moved.
//  2. By parent, name, and class.
//
// Diff does not update or remove a fileless instance that it matches by parent, name, and class.
// See Tree.SetFileless.
func Diff(cur, next *Tree) protocol.Patch {
	var p protocol.Patch

	stored := func(id Ref) bool {
		c, ok := cur.nodes[id]
		return ok && c.inst.Class == next.nodes[id].inst.Class
	}

	var walk func(curID, nextID Ref)
	var place func(id, parent Ref)

	// add puts a new instance and its descendants in p.Added, with the parent before its children.
	// parent is in cur or is earlier in p.Added.
	add := func(id, parent Ref) {
		inst := clone(next.nodes[id].inst)
		inst.Parent = parent
		// If a person changes the class with a file rename, the old instance still uses the ref.
		// The patch removes the old instance after it adds this one, so this one needs a different ref.
		if _, clash := cur.nodes[id]; clash {
			inst.ID = id + "~" + Ref(inst.Class)
		}
		p.Added = append(p.Added, inst)
		for _, child := range next.nodes[id].children {
			place(child, inst.ID)
		}
	}

	// place puts a next instance under parent. A stored ref gives an update, and other refs give an add.
	place = func(id, parent Ref) {
		if !stored(id) {
			add(id, parent)
			return
		}
		c, n := cur.nodes[id].inst, next.nodes[id].inst
		u := protocol.Update{ID: id}
		if c.Parent != parent {
			u.Parent = parent
		}
		if c.Name != n.Name {
			u.Name = n.Name
		}
		if props := changedProps(c.Props, n.Props); len(props) > 0 {
			u.Props = props
		}
		if u.Parent != "" || u.Name != "" || u.Props != nil {
			p.Updated = append(p.Updated, u)
		}
		walk(id, id)
	}

	// walk matches the children of nextID to the children of curID, and then goes down into each match.
	walk = func(curID, nextID Ref) {
		// existing holds the children of curID that do not have a match in next yet.
		existing := map[sibling]Ref{}
		for _, id := range cur.nodes[curID].children {
			if _, inNext := next.nodes[id]; inNext && stored(id) {
				// place finds this child by its ref, at any location in next.
				continue
			}
			inst := cur.nodes[id].inst
			key := sibling{inst.Name, inst.Class}
			// If two siblings have the same key, a file with no stored ref belongs to the sibling that is not fileless.
			prev, taken := existing[key]
			if !taken || (cur.nodes[prev].fileless && !cur.nodes[id].fileless) {
				existing[key] = id
			}
		}
		for _, id := range next.nodes[nextID].children {
			if stored(id) {
				place(id, curID)
				continue
			}
			inst := next.nodes[id].inst
			key := sibling{inst.Name, inst.Class}
			match, found := existing[key]
			if !found {
				add(id, curID)
				continue
			}
			delete(existing, key)
			if cur.nodes[match].fileless {
				// The file on disk is not the file of this instance.
				continue
			}
			if props := changedProps(cur.nodes[match].inst.Props, inst.Props); len(props) > 0 {
				p.Updated = append(p.Updated, protocol.Update{ID: match, Props: props})
			}
			walk(match, id)
		}
		// Each child that is still in existing is in the tree but not on disk.
		for _, id := range existing {
			if !cur.nodes[id].fileless {
				p.Removed = append(p.Removed, id)
			}
		}
	}
	walk(cur.root, next.root)
	return p
}
