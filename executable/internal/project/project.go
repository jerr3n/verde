// Package project reads a Verde project from disk into an instance tree.
//
// Files map to instances like this:
//
//	Main.server.luau         Script
//	Input.client.luau        LocalScript
//	Util.luau                ModuleScript
//	lib/                     Folder
//	Shop/init.server.luau    Shop, as a Script whose children are the rest of Shop/
//	Lamp.Part.inst.json      Lamp, as a Part with the properties in the file
//	Car/init.Model.inst.json  Car, as a Model whose children are the rest of Car/
//	Main.Script.inst.json    Main.server.luau's properties other than Source
//	lib/init.Folder.inst.json  lib's properties, if it has any
//	Part~3f9a1c2e7b04.Part.inst.json  a second Part called Part, with its name inside
//
// Other files are skipped. Names that aren't safe as file names are percent-encoded
// (see encodeName). Load reads this mapping from disk, and Apply writes it.
package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
	"uuid"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

// Load reads every sync target in cfg. It returns the tree and mounts a snapshot
// needs, or an error if anything is wrong. There's no partial result, because a
// snapshot with files missing could make the plugin delete them from disk.
func Load(cfg *config.Cfg) (*tree.Tree, []protocol.Mount, error) {
	for _, pattern := range cfg.Ignore.Paths {
		if !doublestar.ValidatePattern(pattern) {
			return nil, nil, fmt.Errorf("ignore.paths: %q is not a valid pattern", pattern)
		}
	}

	// os.Root refuses anything that leaves the project, including through symlinks.
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()

	game := newRef()
	l := &loader{
		cfg:    cfg,
		files:  root.FS(),
		tree:   tree.New(protocol.Instance{ID: game, Name: "game", Class: "DataModel"}),
		mounts: []protocol.Mount{{ID: game, Path: []string{}, Sync: false}},
		refs:   map[string]protocol.Ref{"": game},
		seen:   map[sibling]string{},
		ids:    map[protocol.Ref]string{},
	}

	for _, entry := range cfg.Sync {
		mount, err := l.addMount(entry.Instance)
		if err != nil {
			return nil, nil, fmt.Errorf("sync %s: %w", entry.Instance, err)
		}
		if err := l.loadMount(filepath.ToSlash(entry.Path), entry.Instance, mount); err != nil {
			return nil, nil, fmt.Errorf("sync %s: %w", entry.Path, err)
		}
	}
	return l.tree, l.mounts, nil
}

func newRef() protocol.Ref {
	return protocol.Ref(uuid.New().String())
}

// classify works out what a file becomes from its name. ok is false if it isn't a
// script. It stops at the first matching suffix, because ".luau" also matches the
// other two.
func classify(filename string) (name, class string, ok bool) {
	for _, kind := range scriptKinds {
		if name, found := strings.CutSuffix(filename, kind.suffix); found {
			return name, kind.class, name != ""
		}
	}
	return "", "", false
}

var scriptKinds = []struct{ suffix, class string }{
	{".server.luau", "Script"},
	{".client.luau", "LocalScript"},
	{".luau", "ModuleScript"},
}

// scriptSuffix returns the file suffix for a script class, or "" for anything else.
func scriptSuffix(class string) string {
	for _, kind := range scriptKinds {
		if kind.class == class {
			return kind.suffix
		}
	}
	return ""
}

// isInit reports whether a file is the script of the folder it's in. It's
// case-sensitive, so Init.luau is a normal ModuleScript.
func isInit(filename string) bool {
	name, _, ok := classify(filename)
	return ok && name == "init"
}

// sibling identifies an instance among its siblings, the way the plugin matches them.
type sibling struct {
	parent      protocol.Ref
	name, class string
}

type loader struct {
	cfg    *config.Cfg
	files  fs.FS // project folder
	tree   *tree.Tree
	mounts []protocol.Mount
	refs   map[string]protocol.Ref // mount path -> Ref, with "" as the DataModel
	seen   map[sibling]string      // instances so far w/ no id -> file each came from
	ids    map[protocol.Ref]string // ids from .inst.json files -> file each came from
}

// addMount adds a sync target from the config, such as
// "StarterPlayer/StarterPlayerScripts", along with any parents on the way to it. It
// returns the target's Ref, so its files can be loaded as children of that instance.
//
// Mounts already exist in the game, so the plugin finds them by path and never
// creates them.
func (l *loader) addMount(instance string) (protocol.Ref, error) {
	names := strings.Split(instance, "/")

	// Start at the DataModel, which Load stores under the empty path.
	parent := l.refs[""]

	// Walk down one level at a time: "StarterPlayer", then
	// "StarterPlayer/StarterPlayerScripts".
	for depth, name := range names {
		key := strings.Join(names[:depth+1], "/")

		// Sync targets can share a parent, which must only be added once.
		ref, seen := l.refs[key]
		if !seen {
			ref = newRef()

			// A mount's class is taken from its name, which works because a service's
			// name matches its class.
			err := l.tree.Insert(protocol.Instance{Name: name, Parent: parent, ID: ref, Class: name})
			if err != nil {
				return "", err
			}

			l.mounts = append(l.mounts, protocol.Mount{
				ID: ref,
				// Clone so this mount doesn't share memory with names.
				Path: slices.Clone(names[:depth+1]),
				// Only the last level is the real sync target. Anything above it is
				// just on the way there, so the plugin won't watch or change it.
				Sync: depth == len(names)-1,
			})
			l.refs[key] = ref
		}

		// The next level goes under this one.
		parent = ref
	}

	// After the loop, parent is the sync target itself.
	return parent, nil
}

// loadMount checks a sync folder, then loads what's inside it under the mount.
func (l *loader) loadMount(dir, instancePath string, mount protocol.Ref) error {
	// A missing folder counts as empty, so a new project can still start.
	info, err := fs.Stat(l.files, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	// The config might point at a file by mistake.
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}

	// A service like Workspace can't become a script.
	init, err := l.findInit(dir)
	if err != nil {
		return err
	}
	if init != "" {
		return fmt.Errorf("%s: a service can't have an init file", dir)
	}

	// Everything inside the folder goes under the mount.
	return l.loadChildren(dir, instancePath, mount)
}

// loadChildren sends each entry in a folder to loadDir or loadFile. fs.ReadDir sorts
// entries by name, which keeps sibling order the same between connects.
func (l *loader) loadChildren(dir, instancePath string, parent protocol.Ref) error {
	entries, err := fs.ReadDir(l.files, dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		// Symlinks could lead outside the project, so they aren't followed.
		if !entry.IsDir() && !entry.Type().IsRegular() {
			continue
		}

		full := path.Join(dir, entry.Name())
		ignored, err := l.pathIgnored(full)
		if err != nil {
			return err
		}
		if ignored {
			continue
		}

		if entry.IsDir() {
			err = l.loadDir(full, decodeName(entry.Name()), instancePath, parent)
		} else {
			err = l.loadFile(full, entry.Name(), instancePath, parent)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// turns a folder into an instance, then loads what's inside. its init file decides what it
// becomes, otherwise it's a Folder
func (l *loader) loadDir(full, name, instancePath string, parent protocol.Ref) error {
	init, err := l.findInit(full)
	if err != nil {
		return err
	}
	class := initClass(init)

	var source string
	var file instFile
	if isInit(init) {
		source = path.Join(full, init)
		file, err = l.readOptionalInst(initProps(full, class), class)
	} else if init != "" {
		file, err = l.readInst(path.Join(full, init), class)
	}
	if err != nil {
		return err
	}
	if file.Name != "" {
		name = file.Name
	}

	// Skipping an ignored folder also skips everything inside it.
	childPath := instancePath + "/" + name
	if l.instanceIgnored(childPath, class) {
		return nil
	}

	ref, err := l.insert(parent, name, class, full, source, file)
	if err != nil {
		return err
	}
	return l.loadChildren(full, childPath, ref)
}

// turns a script or .inst.json file into an instance. skips init files, loadDir used those.
// a script's .inst.json is read w/ its script, so a leftover one can't bring it back
func (l *loader) loadFile(full, filename, instancePath string, parent protocol.Ref) error {
	if name, class, ok := classifyInst(filename); ok {
		if name == "init" || scriptSuffix(class) != "" {
			return nil
		}
		file, err := l.readInst(full, class)
		if err != nil {
			return err
		}
		name = decodeName(name)
		if file.Name != "" {
			name = file.Name
		}
		if l.instanceIgnored(instancePath+"/"+name, class) {
			return nil
		}
		_, err = l.insert(parent, name, class, full, "", file)
		return err
	}

	name, class, ok := classify(filename)
	if !ok || isInit(filename) {
		return nil
	}
	file, err := l.readOptionalInst(scriptProps(full, class), class)
	if err != nil {
		return err
	}
	name = decodeName(name)
	if file.Name != "" {
		name = file.Name
	}
	if l.instanceIgnored(instancePath+"/"+name, class) {
		return nil
	}
	_, err = l.insert(parent, name, class, full, full, file)
	return err
}

// adds an instance to the tree and returns its ref. origin is the file or folder it came
// from. if source is set, that file's text becomes Source
func (l *loader) insert(parent protocol.Ref, name, class, origin, source string, file instFile) (protocol.Ref, error) {
	id := file.ID
	if id != "" {
		// copying a file copies its id, and they can't be the same instance
		if other, exists := l.ids[id]; exists {
			return "", fmt.Errorf("%s and %s have the same id. Remove the id from the copy, and give it another name", other, origin)
		}
		l.ids[id] = origin
	} else {
		// Foo.luau and Foo/init.luau would both be a ModuleScript called Foo. nothing tells
		// them apart, so verde wouldn't know which one a change from studio was for
		key := sibling{parent, name, class}
		if other, exists := l.seen[key]; exists {
			return "", fmt.Errorf("%s and %s are both a %s called %s", other, origin, class, name)
		}
		l.seen[key] = origin
		id = newRef()
	}

	inst := protocol.Instance{ID: id, Parent: parent, Name: name, Class: class, Props: file.Props}

	if source != "" {
		text, err := fs.ReadFile(l.files, source)
		if err != nil {
			return "", err
		}

		// json.Marshal would silently replace invalid bytes, and syncing that text
		// back could corrupt the file.
		if !utf8.Valid(text) {
			return "", fmt.Errorf("%s is not valid UTF-8", source)
		}
		encoded, err := json.Marshal(string(text))
		if err != nil {
			return "", err
		}
		if inst.Props == nil {
			inst.Props = map[string]protocol.Value{}
		}
		inst.Props["Source"] = protocol.Value{Type: "String", Value: encoded}
	}

	if err := l.tree.Insert(inst); err != nil {
		return "", err
	}
	return inst.ID, nil
}

// returns a folder's init file, or "" if it doesn't have one. more than one is an error,
// since a folder can only become one instance
func (l *loader) findInit(dir string) (string, error) {
	entries, err := fs.ReadDir(l.files, dir)
	if err != nil {
		return "", err
	}

	found := ""
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !(isInit(entry.Name()) || isInstInit(entry.Name())) {
			continue
		}
		ignored, err := l.pathIgnored(path.Join(dir, entry.Name()))
		if err != nil {
			return "", err
		}
		if ignored {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("%s has both %s and %s", dir, found, entry.Name())
		}
		found = entry.Name()
	}
	return found, nil
}

// pathIgnored reports whether a path from the project root matches ignore.paths.
func (l *loader) pathIgnored(full string) (bool, error) {
	for _, pattern := range l.cfg.Ignore.Paths {
		matched, err := doublestar.Match(pattern, full)
		if err != nil {
			return false, fmt.Errorf("ignore.paths %q: %w", pattern, err)
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}

// instanceIgnored reports whether an instance matches ignore.instances or
// ignore.classes. These have to be filtered here, because the plugin would still
// create an ignored instance that's in the snapshot.
func (l *loader) instanceIgnored(instancePath, class string) bool {
	return slices.Contains(l.cfg.Ignore.Instances, instancePath) ||
		slices.Contains(l.cfg.Ignore.Classes, class)
}
