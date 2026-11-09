// SPDX-License-Identifier: MPL-2.0

package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jerr3n/verde/executable/internal/protocol"
)

// instSuffix is the end of the name of each file that stores an instance.
// The suffix is longer than ".json". Thus Verde does not read package.lock.json as an instance of the class "lock".
const instSuffix = ".inst.json"

// instFile is the contents of a .inst.json file.
// The file name usually holds the name and the class of the instance.
// If a sibling already uses the file name, Name holds the real name, and ID makes the siblings different.
type instFile struct {
	ID    protocol.Ref              `json:"id,omitempty"`
	Name  string                    `json:"name,omitempty"`
	Props map[string]protocol.Value `json:"props,omitempty"`
}

// tagFor returns the text that Verde adds to a file name when a sibling already uses the name.
// An example is Part~3f9a1c2e7b04.
//   - The tag comes from the ref, not from a counter. Thus a deleted sibling does not cause a rename of the other siblings.
//   - The tag is a hash, because a ref comes from the client and can hold any text.
//   - With 50,000 parts of the same name in one folder, the chance of two equal tags is approximately 1 in 200,000.
func tagFor(id protocol.Ref) string {
	sum := sha256.Sum256([]byte(id))
	return "~" + hex.EncodeToString(sum[:6])
}

// validClass reports whether class is safe in a file name.
// A valid class has only ASCII letters, digits, and "_", and does not start with a digit.
// A dot is not permitted, because a dot separates the name and the class.
func validClass(class string) bool {
	if class == "" || (class[0] >= '0' && class[0] <= '9') {
		return false
	}
	for i := range len(class) {
		c := class[i]
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// classifyInst splits a file name such as Lamp.Part.inst.json into a name and a class.
// The class comes after the last dot, because a name can hold dots.
// classifyInst does not decode the name. Thus "init" stays different from the encoded name "%69nit".
func classifyInst(filename string) (name, class string, ok bool) {
	stem, found := strings.CutSuffix(filename, instSuffix)
	if !found {
		return "", "", false
	}
	dot := strings.LastIndexByte(stem, '.')
	if dot <= 0 || !validClass(stem[dot+1:]) {
		return "", "", false
	}
	return stem[:dot], stem[dot+1:], true
}

// isInstInit reports whether a .inst.json file sets the class of its folder, as init.luau does.
// The .inst.json file of a script does not set the class. It only adds properties to the script.
func isInstInit(filename string) bool {
	name, class, ok := classifyInst(filename)
	return ok && name == "init" && scriptSuffix(class) == ""
}

// initClass returns the class that the init file gives to its folder. With no init file, the class is Folder.
func initClass(init string) string {
	if _, class, ok := classify(init); ok {
		return class
	}
	if _, class, ok := classifyInst(init); ok {
		return class
	}
	return "Folder"
}

// leafSuffix returns the file suffix for an instance that has no children and thus is stored as one file.
func leafSuffix(class string) string {
	if suffix := scriptSuffix(class); suffix != "" {
		return suffix
	}
	return "." + class + instSuffix
}

// initProps returns the path of the .inst.json file for an instance that is stored as the folder dir.
func initProps(dir, class string) string {
	return path.Join(dir, "init."+class+instSuffix)
}

// scriptProps returns the path of the .inst.json file next to the script file.
func scriptProps(file, class string) string {
	return strings.TrimSuffix(file, scriptSuffix(class)) + "." + class + instSuffix
}

// savedProps returns the properties that go in a .inst.json file. It removes two types of properties:
//   - The Source of a script, because Source has its own file.
//   - Each property of type Ref, because refs change on each load. These properties stay only in the tree, until the server stops.
func savedProps(inst protocol.Instance) map[string]protocol.Value {
	out := make(map[string]protocol.Value, len(inst.Props))
	for name, value := range inst.Props {
		if value.Type == "Ref" || (name == "Source" && scriptSuffix(inst.Class) != "") {
			continue
		}
		out[name] = value
	}
	return out
}

// encodeInst writes file as JSON with one property on each line, in sorted order.
// Thus the same instance always gives the same bytes, and a change to one property changes one line.
func encodeInst(file instFile) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// Encode adds a newline at the end. Remove the newline, so that a comma can come next.
	encode := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Truncate(b.Len() - 1)
		return nil
	}
	field := func(name string, value any) error {
		if b.Len() > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "  %q: ", name)
		return encode(value)
	}

	if file.ID != "" {
		if err := field("id", file.ID); err != nil {
			return "", err
		}
	}
	if file.Name != "" {
		if err := field("name", file.Name); err != nil {
			return "", err
		}
	}
	if len(file.Props) > 0 {
		if b.Len() > 0 {
			b.WriteString(",\n")
		}
		b.WriteString("  \"props\": {\n")
		names := slices.Sorted(maps.Keys(file.Props))
		for i, name := range names {
			b.WriteString("    ")
			if err := encode(name); err != nil {
				return "", err
			}
			b.WriteString(": ")
			if err := encode(file.Props[name]); err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
			if i < len(names)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString("  }")
	}

	if b.Len() == 0 {
		return "{}\n", nil
	}
	// Load refuses text that is not valid UTF-8. Thus this file would stop the load of the full project.
	if !utf8.Valid(b.Bytes()) {
		return "", errors.New("properties are not valid UTF-8")
	}
	return "{\n" + b.String() + "\n}\n", nil
}

// decodeInst reads the text of a .inst.json file for an instance of the given class.
// It refuses unknown fields and other small errors.
// This is intentional: if a typo causes Verde to ignore a property, Studio gets the wrong state.
func decodeInst(text []byte, class string) (instFile, error) {
	if !utf8.Valid(text) {
		return instFile{}, errors.New("not valid UTF-8")
	}

	var file instFile
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return instFile{}, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return instFile{}, errors.New("has more than one JSON value")
	}

	for name, value := range file.Props {
		switch {
		case value.Type == "":
			return instFile{}, fmt.Errorf("property %s has no type", name)
		case value.Type == "Ref":
			return instFile{}, fmt.Errorf("property %s: Ref properties can't be stored in files yet", name)
		case name == "Source" && scriptSuffix(class) != "":
			return instFile{}, errors.New("a script's Source belongs in its .luau file")
		}
	}
	return file, nil
}

// readInst reads the .inst.json file at full. The file must exist.
func (l *loader) readInst(full, class string) (instFile, error) {
	text, err := fs.ReadFile(l.files, full)
	if err != nil {
		return instFile{}, err
	}
	file, err := decodeInst(text, class)
	if err != nil {
		return instFile{}, fmt.Errorf("%s: %w", full, err)
	}
	return file, nil
}

// readOptionalInst reads the .inst.json file at full for a script or a folder.
// It returns an empty instFile if the file does not exist, is not a regular file, or matches ignore.paths.
func (l *loader) readOptionalInst(full, class string) (instFile, error) {
	ignored, err := l.pathIgnored(full)
	if err != nil || ignored {
		return instFile{}, err
	}
	info, err := fs.Stat(l.files, full)
	if errors.Is(err, fs.ErrNotExist) {
		return instFile{}, nil
	}
	if err != nil {
		return instFile{}, err
	}
	if !info.Mode().IsRegular() {
		return instFile{}, nil
	}
	return l.readInst(full, class)
}
