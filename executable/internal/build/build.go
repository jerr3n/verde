// SPDX-License-Identifier: MPL-2.0

// Package build writes the instances of a project as a Roblox place file or model file.
//
// It writes the XML formats .rbxlx and .rbxmx. https://dom.rojo.space/xml gives the specification.
// The same tree always gives the same bytes. Thus you can compare a built file with the previous one.
package build

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

// Result tells what a build wrote.
type Result struct {
	Instances int
	// Warnings has one entry for each type of property that the build did not write.
	Warnings []string
}

// WriteFile builds the file at path. It replaces the old file only after it writes the full new file.
// The extension of path sets the format:
//   - A .rbxlx place holds the full tree. instance must be empty.
//   - A .rbxmx model holds the instance at the "/"-separated path instance, for example "Workspace/Car".
func WriteFile(path string, t *tree.Tree, mounts []protocol.Mount, instance string) (Result, error) {
	write, err := chooseFormat(path, t, mounts, instance)
	if err != nil {
		return Result{}, err
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".verde-build-*")
	if err != nil {
		return Result{}, err
	}
	// After the rename, this Remove fails, and the failure causes no problem.
	defer os.Remove(file.Name())
	defer file.Close()

	result, err := write(file)
	if err != nil {
		return Result{}, err
	}
	if err := file.Chmod(0o644); err != nil {
		return Result{}, err
	}
	if err := file.Close(); err != nil {
		return Result{}, err
	}
	return result, os.Rename(file.Name(), path)
}

// chooseFormat returns the write function for the extension of path.
func chooseFormat(path string, t *tree.Tree, mounts []protocol.Mount, instance string) (func(io.Writer) (Result, error), error) {
	switch filepath.Ext(path) {
	case ".rbxlx":
		if instance != "" {
			return nil, errors.New("a place holds the whole game, so choosing an instance only works for .rbxmx")
		}
		return func(w io.Writer) (Result, error) { return Write(w, t) }, nil
	case ".rbxmx":
		if instance == "" {
			return nil, errors.New("a model needs an instance to hold, such as Workspace/Car")
		}
		root, err := findModelRoot(t, mounts, instance)
		if err != nil {
			return nil, err
		}
		return func(w io.Writer) (Result, error) { return WriteModel(w, t, root) }, nil
	case ".rbxl", ".rbxm":
		return nil, errors.New("binary files aren't supported yet, use .rbxlx or .rbxmx")
	}
	return nil, fmt.Errorf("can't tell what to build from %q, use .rbxlx or .rbxmx", path)
}

// findModelRoot finds the instance that a model holds.
// A mount cannot be the model root, because each game already has the mounts.
func findModelRoot(t *tree.Tree, mounts []protocol.Mount, instance string) (protocol.Ref, error) {
	found := findPath(t, t.Root(), instance)
	switch {
	case len(found) == 0:
		return "", fmt.Errorf("nothing is at %s. A path looks like Workspace/Car", instance)
	case len(found) > 1:
		return "", fmt.Errorf("%d instances are at %s, so rename the one you want to build", len(found), instance)
	}
	for _, mount := range mounts {
		if mount.ID == found[0] {
			return "", fmt.Errorf("%s already exists in every game, so it can't be a model. Choose something inside it", instance)
		}
	}
	return found[0], nil
}

// findPath returns each instance at path. It can return more than one, because siblings can have the same name.
// A name can hold a "/". Thus findPath compares each child name with the start of the remaining path.
func findPath(t *tree.Tree, parent protocol.Ref, path string) []protocol.Ref {
	var found []protocol.Ref
	for _, child := range t.Children(parent) {
		inst, ok := t.Get(child)
		if !ok {
			continue
		}
		if path == inst.Name {
			found = append(found, child)
		} else if rest, ok := strings.CutPrefix(path, inst.Name+"/"); ok {
			found = append(found, findPath(t, child, rest)...)
		}
	}
	return found
}

// Write writes the tree as a place.
// The DataModel is not an Item in the file. Thus the children of the DataModel are the top level of the file.
func Write(w io.Writer, t *tree.Tree) (Result, error) {
	return write(w, t, t.Children(t.Root()), "")
}

// WriteModel writes root and its descendants as a model.
// WriteModel does not write a Ref property that points outside root, because the file has no target for it.
// It adds a warning for each of these properties.
func WriteModel(w io.Writer, t *tree.Tree, root protocol.Ref) (Result, error) {
	// Roblox writes this Meta element for models, and the format documentation recommends it.
	return write(w, t, []protocol.Ref{root}, "\t<Meta name=\"ExplicitAutoJoints\">true</Meta>\n")
}

// write writes the roots and their descendants in the XML format. meta goes after the <roblox> tag.
func write(w io.Writer, t *tree.Tree, roots []protocol.Ref, meta string) (Result, error) {
	b := &builder{tree: t, db: database(), referents: map[protocol.Ref]string{}}
	for _, root := range roots {
		b.number(root)
	}

	b.out.WriteString("<roblox version=\"4\">\n")
	b.out.WriteString(meta)
	for _, root := range roots {
		inst, ok := t.Get(root)
		if !ok {
			return Result{}, fmt.Errorf("instance %s not found", root)
		}
		if err := b.item(root, 1, instancePath(t, inst.Parent)); err != nil {
			return Result{}, err
		}
	}
	b.out.WriteString("</roblox>\n")

	if _, err := io.WriteString(w, b.out.String()); err != nil {
		return Result{}, err
	}
	return Result{Instances: len(b.referents), Warnings: b.warnings()}, nil
}

// instancePath returns the path of an instance without the DataModel. Warnings use this path.
func instancePath(t *tree.Tree, id protocol.Ref) string {
	inst, ok := t.Get(id)
	if !ok || id == t.Root() {
		return ""
	}
	if parent := instancePath(t, inst.Parent); parent != "" {
		return parent + "/" + inst.Name
	}
	return inst.Name
}

// builder holds the state of one build.
type builder struct {
	tree      *tree.Tree
	db        *reflection
	referents map[protocol.Ref]string
	out       strings.Builder
	skipped   []*skippedProp
}

// skippedProp counts the instances that lost a property for the same reason.
// Thus a project with 500 Parts prints one warning, not the same line 500 times.
// first is the path of the first instance.
type skippedProp struct {
	message string
	first   string
	count   int
}

// number gives a referent to id and to each of its descendants.
// Only these instances are in the file. Thus a Ref to a different instance has no target.
// Roblox uses "RBX" and a UUID. Verde uses numbers, because most refs change on each load,
// and numbers keep the file the same between builds.
func (b *builder) number(id protocol.Ref) {
	b.referents[id] = fmt.Sprintf("RBX%032X", len(b.referents)+1)
	for _, child := range b.tree.Children(id) {
		b.number(child)
	}
}

// item writes the Item element for id and its descendants. parentPath is the path of the parent, for error messages.
func (b *builder) item(id protocol.Ref, depth int, parentPath string) error {
	inst, ok := b.tree.Get(id)
	if !ok {
		return fmt.Errorf("instance %s not found", id)
	}
	path := inst.Name
	if parentPath != "" {
		path = parentPath + "/" + inst.Name
	}
	if !validText(inst.Name) || !validText(inst.Class) {
		return fmt.Errorf("%s: its name or class has a character XML can't hold", path)
	}

	indent := strings.Repeat("\t", depth)
	fmt.Fprintf(&b.out, "%s<Item class=\"%s\" referent=\"%s\">\n", indent, escape(inst.Class), b.referents[id])
	fmt.Fprintf(&b.out, "%s\t<Properties>\n", indent)
	fmt.Fprintf(&b.out, "%s\t\t<string name=\"Name\">%s</string>\n", indent, escape(inst.Name))

	// Sort the names, so that the same tree gives the same bytes.
	// Two names can have the same saved name, for example Color and Color3uint8. Write only the first one.
	names := make([]string, 0, len(inst.Props))
	for name := range inst.Props {
		names = append(names, name)
	}
	slices.Sort(names)
	written := map[string]bool{}
	for _, name := range names {
		if name == "Name" {
			// The tree and the plugin use instance.Name, not a Name property.
			continue
		}
		element, saved, err := b.property(inst, name, inst.Props[name])
		if err != nil {
			// A script without its text makes the place broken, so stop the build.
			if name == "Source" {
				return fmt.Errorf("%s: %w", path, err)
			}
			b.skip(fmt.Sprintf("%s.%s: %v", inst.Class, name, err), path)
			continue
		}
		if element == "" || written[saved] {
			continue
		}
		written[saved] = true
		fmt.Fprintf(&b.out, "%s\t\t%s\n", indent, element)
	}
	fmt.Fprintf(&b.out, "%s\t</Properties>\n", indent)

	for _, child := range b.tree.Children(id) {
		if err := b.item(child, depth+1, path); err != nil {
			return err
		}
	}
	fmt.Fprintf(&b.out, "%s</Item>\n", indent)
	return nil
}

// skip records that the instance at path lost a property, with the reason in message.
func (b *builder) skip(message, path string) {
	for _, s := range b.skipped {
		if s.message == message {
			s.count++
			return
		}
	}
	b.skipped = append(b.skipped, &skippedProp{message: message, first: path, count: 1})
}

// warnings returns one line for each skippedProp.
func (b *builder) warnings() []string {
	out := make([]string, len(b.skipped))
	for i, s := range b.skipped {
		out[i] = fmt.Sprintf("%s (left out of %s", s.message, s.first)
		if s.count > 1 {
			out[i] += fmt.Sprintf(" and %d more", s.count-1)
		}
		out[i] += ")"
	}
	return out
}

// escape makes text safe inside an element or an attribute.
// It also escapes \r. Without this step, an XML reader changes \r into \n.
func escape(text string) string {
	var out strings.Builder
	// The error is always nil, because a write to a strings.Builder cannot fail.
	xml.EscapeText(&out, []byte(text))
	return out.String()
}

// validText reports whether XML can hold text.
// Do this check first, because xml.EscapeText replaces the characters that XML cannot hold, and gives no error.
func validText(text string) bool {
	if !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		switch {
		case r == '\t', r == '\n', r == '\r':
		case r >= 0x20 && r <= 0xD7FF:
		case r >= 0xE000 && r <= 0xFFFD:
		case r >= 0x10000 && r <= 0x10FFFF:
		default:
			return false
		}
	}
	return true
}
