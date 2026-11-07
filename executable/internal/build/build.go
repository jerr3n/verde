// Package build writes a project's instances as a Roblox place or model file.
//
// It writes the XML formats (.rbxlx and .rbxmx), which https://dom.rojo.space/xml documents.
// The same tree gives the same bytes, so a built file can be compared with the last one.
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

// Result says what a build wrote.
type Result struct {
	Instances int
	Warnings  []string // one for each kind of property that had to be left out
}

// WriteFile builds the file at path, replacing it only once the whole file is written. The
// extension says what to build. A place holds everything, and a model holds the instance at
// the /-separated path in instance, such as "Workspace/Car".
func WriteFile(path string, t *tree.Tree, mounts []protocol.Mount, instance string) (Result, error) {
	write, err := chooseFormat(path, t, mounts, instance)
	if err != nil {
		return Result{}, err
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".verde-build-*")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(file.Name()) // fails harmlessly once it's been renamed
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

// findModelRoot finds the instance a model should hold. Mounts can't be one, because they
// already exist in every game.
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

// findPath returns every instance at a path, since siblings can share a name. A name can hold
// a /, so each child is tried against the start of what's left of the path.
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

// Write writes the tree as a place. The DataModel isn't an Item in the file, so its children
// are the file's top level.
func Write(w io.Writer, t *tree.Tree) (Result, error) {
	return write(w, t, t.Children(t.Root()), "")
}

// WriteModel writes one instance and everything inside it as a model. A Ref to anything
// outside it is left out with a warning, since the file has nothing for it to point at.
func WriteModel(w io.Writer, t *tree.Tree, root protocol.Ref) (Result, error) {
	// Roblox writes this for models, and the format's docs recommend keeping it.
	return write(w, t, []protocol.Ref{root}, "\t<Meta name=\"ExplicitAutoJoints\">true</Meta>\n")
}

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

// instancePath returns an instance's path without the DataModel, to name it in warnings.
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

type builder struct {
	tree      *tree.Tree
	db        *reflection
	referents map[protocol.Ref]string
	out       strings.Builder
	skipped   []*skippedProp
}

// skippedProp is every instance that lost a property for the same reason, because a project
// with 500 Parts shouldn't print the same line 500 times.
type skippedProp struct {
	message string
	first   string
	count   int
}

// number gives an instance and everything inside it a referent. Only these are in the file,
// so a Ref to anything else can't resolve. Roblox uses RBX and a UUID, but numbering them keeps
// the file the same between builds, since most Refs are new on every load.
func (b *builder) number(id protocol.Ref) {
	b.referents[id] = fmt.Sprintf("RBX%032X", len(b.referents)+1)
	for _, child := range b.tree.Children(id) {
		b.number(child)
	}
}

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

	// Sorted so the same tree gives the same bytes. Two names can be saved as one, like
	// Color and Color3uint8, and only the first is written.
	names := make([]string, 0, len(inst.Props))
	for name := range inst.Props {
		names = append(names, name)
	}
	slices.Sort(names)
	written := map[string]bool{}
	for _, name := range names {
		if name == "Name" {
			continue // instance.Name is what the tree and the plugin use
		}
		element, saved, err := b.property(inst, name, inst.Props[name])
		if err != nil {
			// A script without its text isn't a smaller place, it's a broken one.
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

func (b *builder) skip(message, path string) {
	for _, s := range b.skipped {
		if s.message == message {
			s.count++
			return
		}
	}
	b.skipped = append(b.skipped, &skippedProp{message: message, first: path, count: 1})
}

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

// escape makes text safe inside an element or attribute. Unlike writing it by hand, it also
// protects \r, which an XML reader would otherwise turn into \n.
func escape(text string) string {
	var out strings.Builder
	xml.EscapeText(&out, []byte(text)) // a strings.Builder can't fail
	return out.String()
}

// validText reports whether XML can hold text at all. xml.EscapeText replaces what it can't
// hold, which would quietly change the text.
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
