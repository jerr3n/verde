package tree

import (
	"bytes"
	"encoding/json"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

type sibling struct {
	name, class string
}

// OnDisk reports whether a class can exist on disk. Others are only in the tree, so a
// scan not finding them doesn't mean they were deleted
func OnDisk(class string) bool {
	switch class {
	case "Script", "LocalScript", "ModuleScript", "Folder":
		return true
	}
	return false
}

// returns props in next that are new or differ from cur. Props only in cur are kept,
// because a scan can't see things that aren't on disk, like attributes
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
	// Compare decoded text, since the same string can be escaped differently in JSON.
	if a.Type == "String" {
		var x, y string
		if json.Unmarshal(a.Value, &x) == nil && json.Unmarshal(b.Value, &y) == nil {
			return x == y
		}
	}
	return bytes.Equal(a.Value, b.Value)
}

// builds the patch that turns cur into next. Refs change on every scan, so instances
// are matched by parent, name and class, and the patch uses cur's Refs
func Diff(cur, next *Tree) protocol.Patch {
	var p protocol.Patch
	var walk func(curID, nextID Ref)
	walk = func(curID, nextID Ref) {
		// cur's children that next hasn't matched yet.
		existing := map[sibling]Ref{}
		for _, id := range cur.nodes[curID].children {
			inst := cur.nodes[id].inst
			key := sibling{inst.Name, inst.Class}
			// Same-named siblings share one file, which belongs to the one that isn't fileless.
			prev, taken := existing[key]
			if !taken || (cur.nodes[prev].fileless && !cur.nodes[id].fileless) {
				existing[key] = id
			}
		}
		for _, id := range next.nodes[nextID].children {
			inst := next.nodes[id].inst
			key := sibling{inst.Name, inst.Class}
			match, found := existing[key]
			if !found {
				// Walk the subtree parents-first; only the top entry's Parent becomes curID.
				p.Added = next.appendSubtree(p.Added, id, curID)
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
		for key, id := range existing { // on the tree but no longer on disk
			if OnDisk(key.class) && !cur.nodes[id].fileless {
				p.Removed = append(p.Removed, id)
			}
		}
	}
	walk(cur.root, next.root)
	return p
}
