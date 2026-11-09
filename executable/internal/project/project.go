// SPDX-License-Identifier: MPL-2.0

// Package project changes the files in the sync folders into a tree of instances, and writes changes from Studio back to the files.
//
// The file names follow these rules:
//   - Name.server.luau is a Script. Name.client.luau is a LocalScript. Name.luau is a ModuleScript.
//   - A folder is a Folder. If the folder has an init file, the init file sets the class of the folder.
//   - A .inst.json file holds the properties and the stored ref of an instance.
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

// Load reads each sync folder in cfg, and returns the tree and the mounts for a snapshot.
// If one file or folder has a problem, Load returns an error and no tree.
// A tree with missing files is dangerous, because the plugin could then delete those files from disk.
func Load(cfg *config.Cfg) (*tree.Tree, []protocol.Mount, error) {
	for _, pattern := range cfg.Ignore.Paths {
		if !doublestar.ValidatePattern(pattern) {
			return nil, nil, fmt.Errorf("ignore.paths: %q is not a valid pattern", pattern)
		}
	}

	// os.Root refuses each path that goes outside the project, also through a symlink.
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

	for _, entry := range cfg.Sync.Instances {
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

// newRef makes a new random ref.
func newRef() protocol.Ref {
	return protocol.Ref(uuid.New().String())
}

// classify returns the instance name and the script class for a file name.
// ok is false if the file is not a script, or if the name before the suffix is empty.
// classify uses the first suffix that matches, because ".luau" also matches the other two suffixes.
func classify(filename string) (name, class string, ok bool) {
	for _, kind := range scriptKinds {
		if name, found := strings.CutSuffix(filename, kind.suffix); found {
			return name, kind.class, name != ""
		}
	}
	return "", "", false
}

// scriptKinds maps each script file suffix to its class. The longest suffixes come first.
var scriptKinds = []struct{ suffix, class string }{
	{".server.luau", "Script"},
	{".client.luau", "LocalScript"},
	{".luau", "ModuleScript"},
}

// scriptSuffix returns the file suffix for a script class. It returns "" for a class that is not a script.
func scriptSuffix(class string) string {
	for _, kind := range scriptKinds {
		if kind.class == class {
			return kind.suffix
		}
	}
	return ""
}

// isInit reports whether a file is the script of its folder, for example init.server.luau.
// The check is case-sensitive. Thus Init.luau is a usual ModuleScript.
func isInit(filename string) bool {
	name, _, ok := classify(filename)
	return ok && name == "init"
}

// sibling identifies an instance among its siblings. The plugin uses the same key to match instances.
type sibling struct {
	parent      protocol.Ref
	name, class string
}

// loader holds the state of one Load.
type loader struct {
	cfg *config.Cfg
	// files is the project folder.
	files  fs.FS
	tree   *tree.Tree
	mounts []protocol.Mount
	// refs maps each mount path to its ref. The key "" is the DataModel.
	refs map[string]protocol.Ref
	// seen maps each instance without a stored ref to the file or folder that it came from.
	seen map[sibling]string
	// ids maps each stored ref to the file or folder that it came from.
	ids map[protocol.Ref]string
}

// addMount adds a sync target from the config, for example "StarterPlayer/StarterPlayerScripts".
// It also adds each ancestor of the target that is not in the tree yet.
// It returns the ref of the target. The files in the sync folder become children of that instance.
//
// Each mount is already in the game. The plugin finds a mount by its path, and does not create it.
func (l *loader) addMount(instance string) (protocol.Ref, error) {
	names := strings.Split(instance, "/")

	// Start at the DataModel. Load stores the DataModel at the empty path.
	parent := l.refs[""]

	// Go down one level at a time. For example, "StarterPlayer" comes first, and then
	// "StarterPlayer/StarterPlayerScripts".
	for depth, name := range names {
		key := strings.Join(names[:depth+1], "/")

		// Two sync targets can have the same parent. Add each parent only one time.
		ref, seen := l.refs[key]
		if !seen {
			ref = newRef()

			// The class of a mount is the same as its name.
			// This is correct because the name of a service is the same as its class.
			err := l.tree.Insert(protocol.Instance{Name: name, Parent: parent, ID: ref, Class: name})
			if err != nil {
				return "", err
			}

			l.mounts = append(l.mounts, protocol.Mount{
				ID: ref,
				// Copy the slice, so that this mount does not share memory with names.
				Path: slices.Clone(names[:depth+1]),
				// Only the last level is the sync target. Each level above it only holds the target.
				// The plugin does not watch or change those levels.
				Sync: depth == len(names)-1,
			})
			l.refs[key] = ref
		}

		// The next level is a child of this level.
		parent = ref
	}

	// After the loop, parent is the sync target.
	return parent, nil
}

// loadMount checks the sync folder dir, and then loads its contents as children of mount.
func (l *loader) loadMount(dir, instancePath string, mount protocol.Ref) error {
	// A missing folder is the same as an empty folder. Thus a new project can start.
	info, err := fs.Stat(l.files, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	// The config can point at a file by mistake.
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}

	// A service such as Workspace cannot become a script, so a sync folder cannot have an init file.
	init, err := l.findInit(dir)
	if err != nil {
		return err
	}
	if init != "" {
		return fmt.Errorf("%s: a service can't have an init file", dir)
	}

	return l.loadChildren(dir, instancePath, mount)
}

// loadChildren sends each entry in a folder to loadDir or to loadFile.
// fs.ReadDir sorts the entries by name. Thus the order of siblings is the same on each connect.
func (l *loader) loadChildren(dir, instancePath string, parent protocol.Ref) error {
	entries, err := fs.ReadDir(l.files, dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		// A symlink can point outside the project, so Load does not follow symlinks.
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

// loadDir adds a folder as an instance, and then loads its contents.
// The init file of the folder sets the class. A folder without an init file is a Folder.
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

	// If the folder is ignored, its contents are also ignored.
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

// loadFile adds a script file or a .inst.json file as an instance.
// It skips init files, because loadDir reads them.
// It skips the .inst.json file of a script, because it reads that file with the script.
// Thus a .inst.json file that stays after its script is deleted does not add the script again.
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

// insert adds an instance to the tree and returns its ref.
// origin is the file or folder that the instance came from. Error messages use origin.
// If source is not empty, the text of the file source becomes the Source property.
func (l *loader) insert(parent protocol.Ref, name, class, origin, source string, file instFile) (protocol.Ref, error) {
	id := file.ID
	if id != "" {
		// A copy of a file has the same stored ref, but the copy is a different instance.
		if other, exists := l.ids[id]; exists {
			return "", fmt.Errorf("%s and %s have the same id. Remove the id from the copy, and give it another name", other, origin)
		}
		l.ids[id] = origin
	} else {
		// Foo.luau and Foo/init.luau both become a ModuleScript with the name Foo.
		// Nothing makes them different. Thus Verde cannot know which one a change from Studio is for.
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

		// json.Marshal replaces invalid bytes and gives no error.
		// If that text syncs back to disk, the file can become corrupt.
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

// findInit returns the name of the init file in dir, or "" if dir has no init file.
// An init file is an init script or an init .inst.json file. Ignored files do not count.
// More than one init file is an error, because a folder can become only one instance.
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

// pathIgnored reports whether full matches ignore.paths. full is a path from the project root.
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

// instanceIgnored reports whether an instance matches ignore.instances or ignore.classes.
// Load must remove these instances, because the plugin creates each instance that is in the snapshot.
func (l *loader) instanceIgnored(instancePath, class string) bool {
	return slices.Contains(l.cfg.Ignore.Instances, instancePath) ||
		slices.Contains(l.cfg.Ignore.Classes, class)
}
