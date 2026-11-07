// Package build writes a project's instances as a Roblox place file.
//
// It writes the XML format (.rbxlx), which https://dom.rojo.space/xml documents. The same
// tree gives the same bytes, so a built place can be compared with the last one.
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

// WriteFile builds a place at path, replacing it only once the whole file is written. It
// returns a warning for each kind of property that had to be left out.
func WriteFile(path string, t *tree.Tree) ([]string, error) {
	switch ext := filepath.Ext(path); ext {
	case ".rbxlx":
	case ".rbxl":
		return nil, errors.New("binary places aren't supported yet, use .rbxlx")
	default:
		return nil, fmt.Errorf("can't tell what to build from %q, use .rbxlx", path)
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".verde-build-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(file.Name()) // fails harmlessly once it's been renamed
	defer file.Close()

	warnings, err := Write(file, t)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o644); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return warnings, os.Rename(file.Name(), path)
}

// Write writes the tree as a place. The DataModel isn't an Item in the file, so its children
// are the file's top level.
func Write(w io.Writer, t *tree.Tree) ([]string, error) {
	b := &builder{tree: t, db: database(), referents: map[protocol.Ref]string{}}
	for i, inst := range t.All() {
		// Roblox uses RBX and a UUID. Numbering them keeps the file the same between builds,
		// since most Refs are new on every load.
		b.referents[inst.ID] = fmt.Sprintf("RBX%032X", i)
	}

	b.out.WriteString("<roblox version=\"4\">\n")
	for _, child := range t.Children(t.Root()) {
		if err := b.item(child, 1, ""); err != nil {
			return nil, err
		}
	}
	b.out.WriteString("</roblox>\n")

	if _, err := io.WriteString(w, b.out.String()); err != nil {
		return nil, err
	}
	return b.warnings(), nil
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
