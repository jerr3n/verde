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

// ends files that store instances. more specific than .json, so package.lock.json isn't
// read as an instance of class "lock"
const instSuffix = ".inst.json"

// what a .inst.json file holds. name and class are in the file name, unless the name was
// taken. then the real name goes here, w/ an id to tell the siblings apart
type instFile struct {
	ID    protocol.Ref              `json:"id,omitempty"`
	Name  string                    `json:"name,omitempty"`
	Props map[string]protocol.Value `json:"props,omitempty"`
}

// what's added to a file name when the name is taken, like Part~3f9a1c2e7b04. it's from the
// ref and not a counter, so deleting a sibling doesn't rename the others. hashed since a ref
// comes from the client and could hold anything. 50k same-named parts in one folder have
// about a 1 in 200k chance of a clash
func tagFor(id protocol.Ref) string {
	sum := sha256.Sum256([]byte(id))
	return "~" + hex.EncodeToString(sum[:6])
}

// reports whether a class is safe in a file name. dots would mix up the name and class
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

// splits Lamp.Part.inst.json into name and class. class is after the last dot, since names
// can hold dots. name stays as it is on disk, so init isn't mixed up w/ an encoded %69nit
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

// reports whether a file gives its folder a class, like init.luau does. a script's
// .inst.json doesn't, it only adds to the script next to it
func isInstInit(filename string) bool {
	name, class, ok := classifyInst(filename)
	return ok && name == "init" && scriptSuffix(class) == ""
}

// class of a folder, from its init file
func initClass(init string) string {
	if _, class, ok := classify(init); ok {
		return class
	}
	if _, class, ok := classifyInst(init); ok {
		return class
	}
	return "Folder"
}

// what follows the name of an instance with no children
func leafSuffix(class string) string {
	if suffix := scriptSuffix(class); suffix != "" {
		return suffix
	}
	return "." + class + instSuffix
}

// .inst.json file of an instance stored as a folder
func initProps(dir, class string) string {
	return path.Join(dir, "init."+class+instSuffix)
}

// .inst.json file next to a script stored as one file
func scriptProps(file, class string) string {
	return strings.TrimSuffix(file, scriptSuffix(class)) + "." + class + instSuffix
}

// props that go in a .inst.json file. source has its own file, and refs change on every
// load, so those stay in the tree until the server restarts
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

// one prop per line, sorted, so the same instance gives the same bytes and a change is a
// one-line diff
func encodeInst(file instFile) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// Encode ends w/ a newline, which has to go so a comma can follow
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
	// load refuses invalid text, so this would stop the whole project loading
	if !utf8.Valid(b.Bytes()) {
		return "", errors.New("properties are not valid UTF-8")
	}
	return "{\n" + b.String() + "\n}\n", nil
}

// strict on purpose: a typo that dropped a prop would send the wrong state to studio
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

// reads the .inst.json of an instance that has to have one
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

// reads the .inst.json of a script or folder, which may not have one
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
