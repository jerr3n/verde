// Package project reads a Verde project from disk into an instance tree.
//
// Files map to instances like this:
//
//	Main.server.luau         Script
//	Input.client.luau        LocalScript
//	Util.luau                ModuleScript
//	lib/                     Folder
//	Shop/index.server.luau   Shop, as a Script whose children are the rest of Shop/
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

// isIndex reports whether a file is the script of the folder it's in. It's
// case-sensitive, so Index.luau is a normal ModuleScript.
func isIndex(filename string) bool {
	name, _, ok := classify(filename)
	return ok && name == "index"
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
	seen   map[sibling]string      // instances added so far -> the file each came from
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
	index, err := l.findIndex(dir)
	if err != nil {
		return err
	}
	if index != "" {
		return fmt.Errorf("%s: a service can't have an index file", dir)
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

// loadDir turns a folder into an instance, then loads what's inside it. With an
// index file, the folder becomes that script. Otherwise, it's a Folder.
func (l *loader) loadDir(full, name, instancePath string, parent protocol.Ref) error {
	index, err := l.findIndex(full)
	if err != nil {
		return err
	}

	class, source := "Folder", ""
	if index != "" {
		_, class, _ = classify(index)
		source = path.Join(full, index)
	}

	// Skipping an ignored folder also skips everything inside it.
	childPath := instancePath + "/" + name
	if l.instanceIgnored(childPath, class) {
		return nil
	}

	ref, err := l.insert(parent, name, class, source)
	if err != nil {
		return err
	}
	return l.loadChildren(full, childPath, ref)
}

// loadFile turns a script file into an instance. Other files are skipped, and so are
// index files, because loadDir has already used them.
func (l *loader) loadFile(full, filename, instancePath string, parent protocol.Ref) error {
	name, class, ok := classify(filename)
	if !ok || isIndex(filename) {
		return nil
	}
	name = decodeName(name)
	if l.instanceIgnored(instancePath+"/"+name, class) {
		return nil
	}
	_, err := l.insert(parent, name, class, full)
	return err
}

// insert adds an instance to the tree and returns its Ref. If source is set, that
// file's text becomes the instance's Source.
func (l *loader) insert(parent protocol.Ref, name, class, source string) (protocol.Ref, error) {
	inst := protocol.Instance{ID: newRef(), Parent: parent, Name: name, Class: class}

	if source != "" {
		// Foo.luau and Foo/index.luau would both be a ModuleScript called Foo.
		key := sibling{parent, name, class}
		if other, exists := l.seen[key]; exists {
			return "", fmt.Errorf("%s and %s are both a %s called %s", other, source, class, name)
		}
		l.seen[key] = source

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
		inst.Props = map[string]protocol.Value{"Source": {Type: "String", Value: encoded}}
	}

	if err := l.tree.Insert(inst); err != nil {
		return "", err
	}
	return inst.ID, nil
}

// findIndex returns the name of a folder's index file, or "" if it doesn't have one.
// More than one is an error, because a folder can only become one script.
func (l *loader) findIndex(dir string) (string, error) {
	entries, err := fs.ReadDir(l.files, dir)
	if err != nil {
		return "", err
	}

	found := ""
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !isIndex(entry.Name()) {
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
