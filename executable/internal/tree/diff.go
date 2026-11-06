package tree

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

type sibling struct {
	name, class string
}

// returns props in next that are new or differ from cur. props only in cur are kept, since
// a scan can't see refs, and studio leaves a prop missing from a patch as it is
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

// reports whether two values are equal
func sameValue(a, b protocol.Value) bool {
	if a.Type != b.Type {
		return false
	}
	if bytes.Equal(a.Value, b.Value) {
		return true
	}
	// compare decoded, since studio, go and someone editing a file can write the same value
	// differently: other escapes, 1.0 for 1, added spaces
	var x, y any
	if json.Unmarshal(a.Value, &x) != nil || json.Unmarshal(b.Value, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// builds the patch that turns cur into next. refs change on every scan unless a file stores
// one, so instances match by ref first, then parent, name and class, and the patch uses
// cur's refs. a stored ref showing up somewhere else means the file moved
func Diff(cur, next *Tree) protocol.Patch {
	var p protocol.Patch

	stored := func(id Ref) bool {
		c, ok := cur.nodes[id]
		return ok && c.inst.Class == next.nodes[id].inst.Class
	}

	var walk func(curID, nextID Ref)
	var place func(id, parent Ref)

	// adds an instance that's new, parents first, under a parent in cur or in this patch
	add := func(id, parent Ref) {
		inst := clone(next.nodes[id].inst)
		inst.Parent = parent
		// its ref is still in use if someone changed its class by renaming its file, and the
		// old one isn't removed until after this is added
		if _, clash := cur.nodes[id]; clash {
			inst.ID = id + "~" + Ref(inst.Class)
		}
		p.Added = append(p.Added, inst)
		for _, child := range next.nodes[id].children {
			place(child, inst.ID)
		}
	}

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

	walk = func(curID, nextID Ref) {
		// cur's children that next hasn't matched yet.
		existing := map[sibling]Ref{}
		for _, id := range cur.nodes[curID].children {
			if _, inNext := next.nodes[id]; inNext && stored(id) {
				continue // found by its ref, wherever it is
			}
			inst := cur.nodes[id].inst
			key := sibling{inst.Name, inst.Class}
			// a file w/ no id belongs to the one that isn't fileless
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
				continue // whatever's on disk isn't this instance's file
			}
			if props := changedProps(cur.nodes[match].inst.Props, inst.Props); len(props) > 0 {
				p.Updated = append(p.Updated, protocol.Update{ID: match, Props: props})
			}
			walk(match, id)
		}
		for _, id := range existing { // on the tree but no longer on disk
			if !cur.nodes[id].fileless {
				p.Removed = append(p.Removed, id)
			}
		}
	}
	walk(cur.root, next.root)
	return p
}
