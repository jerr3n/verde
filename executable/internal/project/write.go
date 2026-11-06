package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
)

// trashDir holds files that Studio deleted. It's outside every sync folder, so a scan
// never reads it.
const trashDir = ".verde/trash"

// blocked is why an instance can't have a file. Unlike a failed write, trying again
// won't help until something else changes, like the sibling using its name going away.
type blocked string

func (b blocked) Error() string { return string(b) }

const errParentNotOnDisk = blocked("its parent isn't on disk")

// Apply makes a patch from Studio real: it updates the tree, then the files. It returns
// the part of the patch that was applied, for forwarding, and what went wrong.
//
// One bad change doesn't stop the rest, because the plugin has already made all of them
// and doesn't undo any. An instance that can't get a file stays in the tree, marked
// fileless, so that the next scan doesn't take the missing file as a deletion.
func Apply(cfg *config.Cfg, t *tree.Tree, mounts []protocol.Mount, p protocol.Patch) (protocol.Patch, []error) {
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return protocol.Patch{}, []error{err}
	}
	defer root.Close()

	w := &writer{
		tree:     t,
		root:     root,
		scan:     &loader{cfg: cfg, files: root.FS()},
		mounts:   make(map[protocol.Ref]bool, len(mounts)),
		syncDirs: make(map[protocol.Ref]string, len(cfg.Sync)),
	}
	for _, mount := range mounts {
		w.mounts[mount.ID] = true
		if !mount.Sync {
			continue
		}
		for _, entry := range cfg.Sync {
			if entry.Instance == strings.Join(mount.Path, "/") {
				w.syncDirs[mount.ID] = filepath.ToSlash(entry.Path)
			}
		}
	}

	for _, inst := range p.Added {
		w.add(inst)
	}
	for _, u := range p.Updated {
		w.update(u)
	}
	w.remove(p.Removed)
	w.retryFileless()

	// Reported only now, because a later change in the patch can clear the way. Swapping
	// two names, or deleting a script and adding its replacement, isn't a problem.
	for _, f := range w.newlyFileless {
		if t.IsFileless(f.id) {
			w.report(f.id, fmt.Errorf("not saved to disk: %w", f.err))
		}
	}
	return w.applied, w.errs
}

type writer struct {
	tree     *tree.Tree
	root     *os.Root
	scan     *loader // shares the scan's rules for index files and ignores
	mounts   map[protocol.Ref]bool
	syncDirs map[protocol.Ref]string // sync mounts -> their folders

	applied       protocol.Patch
	errs          []error
	newlyFileless []filelessReason
}

type filelessReason struct {
	id  protocol.Ref
	err error
}

// location is where an instance is on disk, as a /-separated path from the project root.
type location struct {
	path string // its file, or its folder if isDir
	file string // the file holding Source, or "" for a Folder
	dir  bool   // path is a folder its children go in
}

func (w *writer) report(id protocol.Ref, err error) {
	w.errs = append(w.errs, fmt.Errorf("%s: %w", w.instancePath(id), err))
}

// instancePath returns an instance's path without game/, like the config's paths. It's
// also what ignore.instances is matched against.
func (w *writer) instancePath(id protocol.Ref) string {
	var names []string
	for {
		inst, ok := w.tree.Get(id)
		if !ok {
			names = append(names, string(id)) // unknown Refs are named in error messages
			break
		}
		if inst.Parent == "" {
			break
		}
		names = append(names, inst.Name)
		id = inst.Parent
	}
	var b strings.Builder
	for i := len(names) - 1; i >= 0; i-- {
		b.WriteString(names[i])
		if i > 0 {
			b.WriteByte('/')
		}
	}
	return b.String()
}

// insideSync reports whether id is a sync target or inside one. The plugin only works
// inside them, so anything else is a bug, and it shouldn't be allowed to touch files.
func (w *writer) insideSync(id protocol.Ref) bool {
	for id != "" {
		if _, ok := w.syncDirs[id]; ok {
			return true
		}
		inst, ok := w.tree.Get(id)
		if !ok {
			return false
		}
		id = inst.Parent
	}
	return false
}

// reachesDisk reports whether an instance's class and all its ancestors can be files.
// A script inside a Part can't yet, since there's nowhere to put the Part.
func (w *writer) reachesDisk(inst protocol.Instance) bool {
	if !tree.OnDisk(inst.Class) {
		return false
	}
	for id := inst.Parent; id != ""; {
		if _, ok := w.syncDirs[id]; ok {
			return true
		}
		parent, ok := w.tree.Get(id)
		if !ok || !tree.OnDisk(parent.Class) {
			return false
		}
		id = parent.Parent
	}
	return false
}

// checkSource drops a Source that isn't text, since it couldn't be written to a file.
func (w *writer) checkSource(id protocol.Ref, class string, props map[string]protocol.Value) {
	if value, ok := props["Source"]; ok && scriptSuffix(class) != "" && value.Type != "String" {
		delete(props, "Source")
		w.report(id, fmt.Errorf("Source must be String, not %s", value.Type))
	}
}

func (w *writer) add(inst protocol.Instance) {
	if !w.insideSync(inst.Parent) {
		w.errs = append(w.errs, fmt.Errorf("%s: its parent isn't inside a sync target", inst.Name))
		return
	}

	w.checkSource(inst.ID, inst.Class, inst.Props)
	// Every script has a file, so it has a Source. Without this, the next scan would
	// find an empty Source the tree doesn't have and send it to Studio.
	if _, ok := inst.Props["Source"]; !ok && scriptSuffix(inst.Class) != "" {
		if inst.Props == nil {
			inst.Props = map[string]protocol.Value{}
		}
		inst.Props["Source"] = protocol.Value{Type: "String", Value: json.RawMessage(`""`)}
	}

	if err := w.tree.Insert(inst); err != nil {
		w.errs = append(w.errs, fmt.Errorf("%s: %w", inst.Name, err))
		return
	}
	w.applied.Added = append(w.applied.Added, inst)
	w.place(inst.ID)
}

func (w *writer) update(u protocol.Update) {
	before, ok := w.tree.Get(u.ID)
	if !ok {
		w.report(u.ID, tree.ErrNotFound)
		return
	}
	if w.mounts[u.ID] && (u.Name != "" || u.Parent != "") {
		w.report(u.ID, errors.New("a mount can't be renamed or moved"))
		return
	}
	if u.Parent != "" && !w.insideSync(u.Parent) {
		w.report(u.ID, errors.New("its new parent isn't inside a sync target"))
		return
	}
	w.checkSource(u.ID, before.Class, u.Props)

	// Has to come before the tree changes, because it follows the old names.
	old, wasOnDisk := w.locate(u.ID)

	if err := w.tree.Update(u); err != nil {
		w.report(u.ID, err)
		return
	}

	renamed := u.Name != "" && u.Name != before.Name
	moved := u.Parent != "" && u.Parent != before.Parent
	if renamed || moved {
		if !wasOnDisk {
			w.place(u.ID)
		} else if err := w.relocate(u.ID, old); err != nil {
			// The file stayed where it was. Keeping the old place in the tree means the
			// next scan still matches it, instead of seeing a new instance and a deletion.
			w.report(u.ID, err)
			revert := protocol.Update{ID: u.ID, Name: before.Name, Parent: before.Parent}
			if err := w.tree.Update(revert); err != nil {
				w.report(u.ID, err)
			}
			u.Name, u.Parent = "", ""
		}
	}

	if _, changed := u.Props["Source"]; changed && scriptSuffix(before.Class) != "" {
		w.writeSource(u.ID)
	}

	if u.Name != "" || u.Parent != "" || len(u.Props) > 0 {
		w.applied.Updated = append(w.applied.Updated, u)
	}
}

// relocate puts an instance's file or folder where the tree now says it belongs. A
// folder is renamed whole, so files that aren't instances, like a README, go with it.
// An error means nothing moved.
func (w *writer) relocate(id protocol.Ref, old location) error {
	inst, _ := w.tree.Get(id)
	if !w.reachesDisk(inst) {
		return w.trash(old.path)
	}

	target, err := w.relocateTarget(inst, old)
	var why blocked
	if errors.As(err, &why) {
		if err := w.trash(old.path); err != nil {
			return err
		}
		w.markFileless(id, why)
		return nil
	}
	if err != nil {
		return err
	}
	if target == old.path {
		return nil
	}
	return w.root.Rename(old.path, target)
}

func (w *writer) relocateTarget(inst protocol.Instance, old location) (string, error) {
	if err := w.checkAllowed(inst); err != nil {
		return "", err
	}
	dir, err := w.childDir(inst.Parent)
	if err != nil {
		return "", err
	}

	name := encodeName(inst.Name)
	if !old.dir {
		name += scriptSuffix(inst.Class)
	}
	target := path.Join(dir, name)
	if target == old.path {
		return target, nil
	}
	if err := w.checkPath(target); err != nil {
		return "", err
	}
	// A case-only rename finds itself on a case-insensitive filesystem.
	if !strings.EqualFold(target, old.path) {
		if err := w.checkFree(target); err != nil {
			return "", err
		}
	}
	return target, nil
}

// remove handles a patch's removals. The plugin lists descendants too, in no particular
// order, so only the top of each subtree touches the disk.
func (w *writer) remove(ids []protocol.Ref) {
	removing := make(map[protocol.Ref]bool, len(ids))
	for _, id := range ids {
		removing[id] = true
	}

	var files []string
	for _, id := range ids {
		if w.mounts[id] {
			w.report(id, errors.New("a mount can't be removed"))
			delete(removing, id)
			continue
		}
		inst, ok := w.tree.Get(id)
		if !ok {
			continue
		}
		top := true
		for parent := inst.Parent; parent != ""; {
			if removing[parent] {
				top = false
				break
			}
			ancestor, _ := w.tree.Get(parent)
			parent = ancestor.Parent
		}
		if top {
			if loc, onDisk := w.locate(id); onDisk {
				files = append(files, loc.path)
			}
		}
	}

	for _, id := range ids {
		if !removing[id] {
			continue
		}
		if err := w.tree.Remove(id); err != nil {
			w.report(id, err)
			continue
		}
		w.applied.Removed = append(w.applied.Removed, id)
	}

	for _, file := range files {
		// The file stays, so the next scan brings the instance back. That undoes the
		// user's delete, but it loses nothing, and they get this error to explain it.
		if err := w.trash(file); err != nil {
			w.errs = append(w.errs, fmt.Errorf("remove %s: %w", file, err))
		}
	}
}

// retryFileless gives fileless instances another chance, since this patch may have
// removed what was in the way. Each one's error was already reported.
func (w *writer) retryFileless() {
	for _, id := range w.tree.Fileless() {
		if w.tree.IsFileless(id) { // placing its parent may have placed it already
			w.place(id)
		}
	}
}

// place puts an instance and everything under it on disk, or marks it fileless.
func (w *writer) place(id protocol.Ref) {
	inst, ok := w.tree.Get(id)
	if !ok {
		return
	}
	if !w.reachesDisk(inst) {
		w.tree.SetFileless(id, false) // the scan doesn't look here, so there's no need
		return
	}
	if err := w.createOnDisk(inst); err != nil {
		w.markFileless(id, err)
		return
	}
	w.tree.SetFileless(id, false)
	for _, child := range w.tree.Children(id) {
		w.place(child)
	}
}

func (w *writer) markFileless(id protocol.Ref, err error) {
	// Its parent's error already explains this, and a retry shouldn't repeat itself.
	if !w.tree.IsFileless(id) && !errors.Is(err, errParentNotOnDisk) {
		w.newlyFileless = append(w.newlyFileless, filelessReason{id, err})
	}
	w.tree.SetFileless(id, true)
}

func (w *writer) createOnDisk(inst protocol.Instance) error {
	if err := w.checkAllowed(inst); err != nil {
		return err
	}
	dir, err := w.childDir(inst.Parent)
	if err != nil {
		return err
	}

	// A file can already be here if an earlier write failed, or if it was just created
	// and hasn't been scanned yet. The tree wins, but the file's text goes to the trash.
	if loc, found := w.find(dir, inst); found {
		if loc.file == "" {
			return nil
		}
		text, err := sourceText(inst)
		if err != nil {
			return err
		}
		if current, err := w.root.ReadFile(loc.file); err == nil && string(current) != text {
			if err := w.trash(loc.file); err != nil {
				return err
			}
		}
		return w.writeFile(loc.file, text)
	}

	target := path.Join(dir, encodeName(inst.Name)+scriptSuffix(inst.Class))
	if err := w.checkPath(target); err != nil {
		return err
	}
	if err := w.checkFree(target); err != nil {
		return err
	}
	if inst.Class == "Folder" {
		return w.root.Mkdir(target, 0o755)
	}
	text, err := sourceText(inst)
	if err != nil {
		return err
	}
	return w.writeFile(target, text)
}

// writeSource writes an instance's Source to its file. If that fails, the instance is
// marked fileless, or the next scan would send the file's old text back over the edit.
func (w *writer) writeSource(id protocol.Ref) {
	if w.tree.IsFileless(id) {
		return // retryFileless writes it with the rest
	}
	loc, onDisk := w.locate(id)
	if !onDisk {
		w.place(id) // its file is gone, so this makes a new one
		return
	}
	inst, _ := w.tree.Get(id)
	text, err := sourceText(inst)
	if err == nil {
		err = w.writeFile(loc.file, text)
	}
	if err != nil {
		w.markFileless(id, err)
	}
}

// checkAllowed checks the rules that don't depend on where the instance's file would go.
func (w *writer) checkAllowed(inst protocol.Instance) error {
	if inst.Name == "" {
		return blocked("it has no name")
	}
	if w.scan.instanceIgnored(w.instancePath(inst.ID), inst.Class) {
		return blocked("it matches ignore.instances or ignore.classes")
	}
	// Files can't share a name. This ignores case, because on Windows and macOS Foo and
	// foo are the same file, and one would overwrite the other.
	for _, id := range w.tree.Children(inst.Parent) {
		other, _ := w.tree.Get(id)
		if id != inst.ID && other.Class == inst.Class && strings.EqualFold(other.Name, inst.Name) &&
			!w.tree.IsFileless(id) {
			return blocked(fmt.Sprintf("another %s next to it is called %s, and they can't share a file", other.Class, other.Name))
		}
	}
	return nil
}

func (w *writer) checkPath(target string) error {
	ignored, err := w.scan.pathIgnored(target)
	if err != nil {
		return err
	}
	if ignored {
		return blocked(target + " matches ignore.paths")
	}
	return nil
}

func (w *writer) checkFree(target string) error {
	if _, err := w.root.Lstat(target); err == nil {
		return blocked(target + " already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// locate finds an instance on disk from the tree and what's in each folder. There's no
// stored mapping that could go stale when someone changes the files themselves.
func (w *writer) locate(id protocol.Ref) (location, bool) {
	if dir, ok := w.syncDirs[id]; ok {
		return location{path: dir, dir: true}, true
	}
	inst, ok := w.tree.Get(id)
	if !ok || w.tree.IsFileless(id) || !tree.OnDisk(inst.Class) {
		return location{}, false
	}
	parent, ok := w.locate(inst.Parent)
	if !ok || !parent.dir {
		return location{}, false
	}
	return w.find(parent.path, inst)
}

// find looks through a folder for an instance. It decodes each entry, rather than
// encoding the name and checking one path, because a file from outside Verde may hold a
// character that encodeName would have escaped, like What?.luau.
func (w *writer) find(dir string, inst protocol.Instance) (location, bool) {
	entries, err := fs.ReadDir(w.scan.files, dir)
	if err != nil {
		return location{}, false
	}

	suffix := scriptSuffix(inst.Class)
	for _, entry := range entries {
		full := path.Join(dir, entry.Name())
		switch {
		case entry.IsDir() && decodeName(entry.Name()) == inst.Name:
			index, err := w.scan.findIndex(full)
			if err != nil {
				continue
			}
			if index == "" && inst.Class == "Folder" {
				return location{path: full, dir: true}, true
			}
			if suffix != "" && index == "index"+suffix {
				return location{path: full, file: path.Join(full, index), dir: true}, true
			}
		case entry.Type().IsRegular() && suffix != "":
			name, class, ok := classify(entry.Name())
			if ok && !isIndex(entry.Name()) && class == inst.Class && decodeName(name) == inst.Name {
				return location{path: full, file: full}, true
			}
		}
	}
	return location{}, false
}

// childDir returns the folder an instance's children go in, creating it if needed. A
// script stored as Foo.luau becomes Foo/index.luau the first time it needs one.
func (w *writer) childDir(id protocol.Ref) (string, error) {
	if dir, ok := w.syncDirs[id]; ok {
		return dir, w.root.MkdirAll(dir, 0o755)
	}
	loc, ok := w.locate(id)
	if !ok {
		return "", errParentNotOnDisk
	}
	if loc.dir {
		return loc.path, nil
	}

	inst, _ := w.tree.Get(id)
	suffix := scriptSuffix(inst.Class)
	dir := strings.TrimSuffix(loc.path, suffix)
	index := path.Join(dir, "index"+suffix)
	if err := w.checkPath(dir); err != nil {
		return "", err
	}
	if err := w.checkPath(index); err != nil {
		return "", err
	}
	// Foo/ may belong to a Folder or another script called Foo. Merging them would
	// change what the scan reads.
	if err := w.checkFree(dir); err != nil {
		return "", err
	}
	if err := w.root.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	if err := w.root.Rename(loc.path, index); err != nil {
		w.root.Remove(dir)
		return "", err
	}
	return dir, nil
}

// writeFile replaces a file's contents all at once, by writing a temporary file and
// renaming it, so that a crash can't leave half a script. It leaves an unchanged file
// alone, so editors don't think it changed. The temporary name isn't a script, so a
// scan skips one left behind by a crash.
func (w *writer) writeFile(name, text string) error {
	if current, err := w.root.ReadFile(name); err == nil && string(current) == text {
		return nil
	}

	mode := fs.FileMode(0o644)
	if info, err := w.root.Stat(name); err == nil {
		mode = info.Mode().Perm()
	}

	temp := path.Join(path.Dir(name), ".verde-"+string(newRef())+".tmp")
	file, err := w.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = file.WriteString(text)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = w.root.Rename(temp, name)
	}
	if err != nil {
		w.root.Remove(temp)
	}
	return err
}

// trash moves a file or folder into the trash instead of deleting it, because a delete
// from Studio can destroy work that was never committed.
func (w *writer) trash(name string) error {
	if err := w.root.MkdirAll(trashDir, 0o755); err != nil {
		return err
	}
	// Keeps deleted scripts out of git without the project needing to do anything.
	w.root.WriteFile(path.Join(trashDir, ".gitignore"), []byte("*\n"), 0o644)

	// Each call gets its own folder, so the same path can be trashed more than once.
	target := path.Join(trashDir, time.Now().UTC().Format("20060102-150405.000000000"), name)
	if err := w.root.MkdirAll(path.Dir(target), 0o755); err != nil {
		return err
	}
	return w.root.Rename(name, target)
}

// sourceText returns a script's Source as text.
func sourceText(inst protocol.Instance) (string, error) {
	value, ok := inst.Props["Source"]
	if !ok {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(value.Value, &text); err != nil {
		return "", fmt.Errorf("Source: %w", err)
	}
	return text, nil
}
