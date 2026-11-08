// SPDX-License-Identifier: MPL-2.0

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

	w := newWriter(cfg, t, mounts, root)

	for _, inst := range p.Added {
		w.add(inst)
	}
	for _, u := range p.Updated {
		w.update(u)
	}
	w.remove(p.Removed)
	w.retryFileless()
	if len(p.Updated)+len(p.Removed) > 0 {
		w.dropUnneededTags()
	}

	// Reported only now, because a later change in the patch can clear the way. Swapping
	// two names, or deleting a script and adding its replacement, isn't a problem.
	for _, f := range w.newlyFileless {
		if t.IsFileless(f.id) {
			w.report(f.id, fmt.Errorf("not saved to disk: %w", f.err))
		}
	}
	return w.applied, w.errs
}

// Locate finds an instance's files. Found is false if it has none, like a mount that's
// only on the way to a sync target, or a fileless instance.
func Locate(cfg *config.Cfg, t *tree.Tree, mounts []protocol.Mount, id protocol.Ref) (protocol.Located, error) {
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return protocol.Located{}, err
	}
	defer root.Close()

	w := newWriter(cfg, t, mounts, root)
	loc, ok := w.locate(id)
	if !ok {
		return protocol.Located{}, nil
	}
	located := protocol.Located{Found: true, Path: loc.path, File: loc.file, Dir: loc.dir}
	if loc.props != "" && w.exists(loc.props) {
		located.Props = loc.props
	}
	return located, nil
}

func newWriter(cfg *config.Cfg, t *tree.Tree, mounts []protocol.Mount, root *os.Root) *writer {
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
	return w
}

type writer struct {
	tree     *tree.Tree
	root     *os.Root
	scan     *loader // shares the scan's rules for init files and ignores
	mounts   map[protocol.Ref]bool
	syncDirs map[protocol.Ref]string // sync mounts -> their folders

	applied       protocol.Patch
	errs          []error
	newlyFileless []filelessReason
	tagged        []protocol.Ref // given a tag by this patch, since their name was taken
}

type filelessReason struct {
	id  protocol.Ref
	err error
}

// where an instance is on disk, as /-separated paths from the project root
type location struct {
	path  string // its file, or its folder if dir
	file  string // file holding Source, "" if it isn't a script
	props string // its .inst.json, which a script or folder may not have
	dir   bool   // path is a folder its children go in
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
	// a move can land on a .inst.json left behind by a deleted script
	if renamed || moved || changesSavedProps(before.Class, u.Props) {
		w.saveProps(u.ID)
	}

	if u.Name != "" || u.Parent != "" || len(u.Props) > 0 {
		w.applied.Updated = append(w.applied.Updated, u)
	}
}

// reports whether an update touches anything but a script's source
func changesSavedProps(class string, props map[string]protocol.Value) bool {
	for name := range props {
		if name != "Source" || scriptSuffix(class) == "" {
			return true
		}
	}
	return false
}

// relocate puts an instance's file or folder where the tree now says it belongs. A
// folder is renamed whole, so files that aren't instances, like a README, go with it.
// An error means nothing moved.
func (w *writer) relocate(id protocol.Ref, old location) error {
	inst, _ := w.tree.Get(id)

	target, err := w.relocateTarget(inst, old)
	var why blocked
	if errors.As(err, &why) {
		if err := w.trashLocation(old); err != nil {
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
	if err := w.root.Rename(old.path, target); err != nil {
		return err
	}
	if w.hasSeparateProps(old) {
		if err := w.root.Rename(old.props, scriptProps(target, inst.Class)); err != nil {
			w.root.Rename(target, old.path)
			return err
		}
	}
	return nil
}

// reports whether a script stored as one file has a .inst.json next to it, which has to move
// with it. in a folder, it moves w/ the folder
func (w *writer) hasSeparateProps(loc location) bool {
	if loc.dir || loc.file == "" {
		return false
	}
	_, err := w.root.Lstat(loc.props)
	return err == nil
}

// moves an instance's file or folder to the trash
func (w *writer) trashLocation(loc location) error {
	separate := w.hasSeparateProps(loc)
	if err := w.trash(loc.path); err != nil {
		return err
	}
	if separate {
		return w.trash(loc.props)
	}
	return nil
}

func (w *writer) relocateTarget(inst protocol.Instance, old location) (string, error) {
	if err := w.checkAllowed(inst); err != nil {
		return "", err
	}
	dir, err := w.childDir(inst.Parent)
	if err != nil {
		return "", err
	}
	return w.chooseTarget(dir, inst, old.dir, old.path)
}

// picks the path for an instance's file, or folder if asDir. plain name if it's free, name
// plus tag if not, and then the file holds the real name and id. current doesn't count as
// taken
func (w *writer) chooseTarget(dir string, inst protocol.Instance, asDir bool, current string) (string, error) {
	suffix := ""
	if !asDir {
		suffix = leafSuffix(inst.Class)
	}
	name := encodeName(inst.Name)

	plain := path.Join(dir, name+suffix)
	if plain == current {
		return plain, nil
	}
	if err := w.checkPath(plain); err != nil {
		return "", err
	}
	// A case-only rename finds itself on a case-insensitive filesystem.
	taken := false
	if !strings.EqualFold(plain, current) {
		if _, err := w.root.Lstat(plain); err == nil {
			taken = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if !taken && !w.takenInOtherCase(dir, name, inst.Class, current) {
		return plain, nil
	}

	tagged := path.Join(dir, name+tagFor(inst.ID)+suffix)
	if tagged == current {
		return tagged, nil
	}
	if err := w.checkPath(tagged); err != nil {
		return "", err
	}
	// its real name goes there, so scans have to be able to read it
	if err := w.checkPath(propsFileFor(tagged, inst.Class, asDir)); err != nil {
		return "", err
	}
	if err := w.checkFree(tagged); err != nil {
		return "", err
	}
	w.tagged = append(w.tagged, inst.ID)
	return tagged, nil
}

// .inst.json file of an instance stored at target
func propsFileFor(target, class string, asDir bool) string {
	switch {
	case asDir:
		return initProps(target, class)
	case scriptSuffix(class) != "":
		return scriptProps(target, class)
	}
	return target
}

// reports whether a sibling of the same class has this name, ignoring case or as a folder,
// which lstat can't see. Foo and foo are the same file on windows and macos
func (w *writer) takenInOtherCase(dir, name, class, current string) bool {
	entries, err := fs.ReadDir(w.scan.files, dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		stem, ok := entryStem(entry, class)
		if !ok || !strings.EqualFold(stem, name) {
			continue
		}
		full := path.Join(dir, entry.Name())
		if full == current {
			continue
		}
		if !entry.IsDir() {
			return true
		}
		if init, err := w.scan.findInit(full); err == nil && initClass(init) == class {
			return true
		}
	}
	return false
}

// moves instances tagged by this patch to their plain names, if those are free by now.
// swapping two names, or replacing a script, only needs a tag halfway through
func (w *writer) dropUnneededTags() {
	for _, id := range w.tagged {
		if _, ok := w.tree.Get(id); !ok {
			continue
		}
		loc, onDisk := w.locate(id)
		if !onDisk {
			continue
		}
		if err := w.relocate(id, loc); err != nil {
			w.report(id, err)
			continue
		}
		w.saveProps(id)
	}
}

// remove handles a patch's removals. The plugin lists descendants too, in no particular
// order, so only the top of each subtree touches the disk.
func (w *writer) remove(ids []protocol.Ref) {
	removing := make(map[protocol.Ref]bool, len(ids))
	for _, id := range ids {
		removing[id] = true
	}

	var locations []location
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
				locations = append(locations, loc)
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

	for _, loc := range locations {
		// The file stays, so the next scan brings the instance back. That undoes the
		// user's delete, but it loses nothing, and they get this error to explain it.
		if err := w.trashLocation(loc); err != nil {
			w.errs = append(w.errs, fmt.Errorf("remove %s: %w", loc.path, err))
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
		taken, err := w.claimedBySibling(dir, inst, loc)
		if err != nil {
			return err
		}
		if !taken {
			if loc.file != "" {
				text, err := sourceText(inst)
				if err != nil {
					return err
				}
				if err := w.replaceFile(loc.file, text); err != nil {
					return err
				}
			}
			return w.writeProps(inst, loc, w.replaceFile)
		}
	}

	target, err := w.chooseTarget(dir, inst, inst.Class == "Folder", "")
	if err != nil {
		return err
	}

	var loc location
	switch {
	case inst.Class == "Folder":
		if err := w.root.Mkdir(target, 0o755); err != nil {
			return err
		}
		loc = location{path: target, props: initProps(target, inst.Class), dir: true}
	case scriptSuffix(inst.Class) != "":
		text, err := sourceText(inst)
		if err != nil {
			return err
		}
		if err := w.writeFile(target, text); err != nil {
			return err
		}
		loc = location{path: target, file: target, props: scriptProps(target, inst.Class)}
	default:
		loc = location{path: target, props: target}
	}
	return w.writeProps(inst, loc, w.replaceFile)
}

// reports whether a file w/ no id, found for a new instance, is really a same-named
// sibling's. if so, it gets that sibling's id, so a later scan can tell them apart. a file
// w/ no id always belongs to the only instance with its name and class
func (w *writer) claimedBySibling(dir string, inst protocol.Instance, loc location) (bool, error) {
	if w.readFileMeta(loc, inst.Class).ID != "" {
		return false, nil
	}
	for _, id := range w.tree.Children(inst.Parent) {
		other, _ := w.tree.Get(id)
		if id == inst.ID || other.Class != inst.Class || other.Name != inst.Name || w.tree.IsFileless(id) {
			continue
		}
		// it has its own tagged file, so this one isn't its
		if tagged := path.Join(dir, encodeName(other.Name)+tagFor(id)); w.exists(tagged) || w.exists(tagged+leafSuffix(other.Class)) {
			continue
		}
		if err := w.checkPath(loc.props); err != nil {
			return false, err
		}
		file := instFile{ID: id, Props: savedProps(other)}
		text, err := encodeInst(file)
		if err != nil {
			return false, err
		}
		return true, w.writeFile(loc.props, text)
	}
	return false, nil
}

func (w *writer) exists(name string) bool {
	_, err := w.root.Lstat(name)
	return err == nil
}

// what a location's .inst.json says, if it has one that can be read
func (w *writer) readFileMeta(loc location, class string) instFile {
	file, err := w.scan.readOptionalInst(loc.props, class)
	if err != nil {
		return instFile{}
	}
	return file
}

// writes a file, trashing different text that's already there
func (w *writer) replaceFile(name, text string) error {
	if current, err := w.root.ReadFile(name); err == nil && string(current) != text {
		if err := w.trash(name); err != nil {
			return err
		}
	}
	return w.writeFile(name, text)
}

// writes an instance's props to its .inst.json. marks it fileless on failure, or the next
// scan would send the old values back over the change. skips mounts, service props aren't
// synced
func (w *writer) saveProps(id protocol.Ref) {
	if w.mounts[id] || w.tree.IsFileless(id) {
		return
	}
	loc, onDisk := w.locate(id)
	if !onDisk {
		w.place(id) // its file is gone, so this makes a new one
		return
	}
	inst, _ := w.tree.Get(id)
	if err := w.writeProps(inst, loc, w.writeFile); err != nil {
		w.markFileless(id, err)
	}
}

// writes the .inst.json at loc. a script or folder only has one if there's something to
// store, and goes without if ignore.paths matches, since its other file says it exists.
// anything else has nowhere else to exist
func (w *writer) writeProps(inst protocol.Instance, loc location, write func(name, text string) error) error {
	stem := path.Base(loc.path)
	if !loc.dir {
		stem = strings.TrimSuffix(stem, leafSuffix(inst.Class))
	}

	file := instFile{Props: savedProps(inst)}
	if decodeName(stem) != inst.Name {
		file.Name = inst.Name
	}
	switch {
	case strings.HasSuffix(stem, tagFor(inst.ID)):
		file.ID = inst.ID
	case w.readFileMeta(loc, inst.Class).ID != "" && w.hasTwin(inst):
		file.ID = inst.ID // first of its name, siblings still need telling apart
	}

	optional := loc.file != "" || (loc.dir && inst.Class == "Folder")
	if optional && file.ID == "" && file.Name == "" {
		ignored, err := w.scan.pathIgnored(loc.props)
		if err != nil {
			return err
		}
		if ignored {
			return nil
		}
		if len(file.Props) == 0 {
			if _, err := w.root.Lstat(loc.props); errors.Is(err, fs.ErrNotExist) {
				return nil
			} else if err != nil {
				return err
			}
			return w.trash(loc.props) // the next scan would send its values to studio
		}
	}
	text, err := encodeInst(file)
	if err != nil {
		return err
	}
	return write(loc.props, text)
}

// reports whether a sibling has the same name and class
func (w *writer) hasTwin(inst protocol.Instance) bool {
	for _, id := range w.tree.Children(inst.Parent) {
		other, _ := w.tree.Get(id)
		if id != inst.ID && other.Class == inst.Class && other.Name == inst.Name {
			return true
		}
	}
	return false
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
	if !validClass(inst.Class) {
		return blocked(fmt.Sprintf("its class %q can't be part of a file name", inst.Class))
	}
	if w.scan.instanceIgnored(w.instancePath(inst.ID), inst.Class) {
		return blocked("it matches ignore.instances or ignore.classes")
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
	if !ok || w.tree.IsFileless(id) {
		return location{}, false
	}
	parent, ok := w.locate(inst.Parent)
	if !ok || !parent.dir {
		return location{}, false
	}
	if loc, found := w.find(parent.path, inst); found {
		return loc, true
	}
	return w.search(parent.path, inst)
}

// looks through a folder for an instance by its tag or name. decodes each entry instead of
// checking one encoded path, since a file from outside verde may hold a character
// encodeName would've escaped, like What?.luau
func (w *writer) find(dir string, inst protocol.Instance) (location, bool) {
	entries, err := fs.ReadDir(w.scan.files, dir)
	if err != nil {
		return location{}, false
	}
	tag := tagFor(inst.ID)
	var named location
	foundNamed := false
	for _, entry := range entries {
		stem, ok := entryStem(entry, inst.Class)
		if !ok {
			continue
		}
		tagged := strings.HasSuffix(stem, tag)
		if !tagged && (foundNamed || decodeName(stem) != inst.Name) {
			continue
		}
		loc, ok := w.entryLocation(dir, entry, inst.Class)
		if !ok || !w.holds(loc, stem, inst) {
			continue
		}
		// its own tag beats a file w/ its name, which may be a sibling's
		if tagged {
			return loc, true
		}
		named, foundNamed = loc, true
	}
	return named, foundNamed
}

// find for a file whose name says nothing about what's in it, because someone renamed it.
// opens every file of the class, so it's only for when find came up empty and the instance
// should have a file
func (w *writer) search(dir string, inst protocol.Instance) (location, bool) {
	entries, err := fs.ReadDir(w.scan.files, dir)
	if err != nil {
		return location{}, false
	}
	for _, entry := range entries {
		stem, ok := entryStem(entry, inst.Class)
		if !ok {
			continue
		}
		if loc, ok := w.entryLocation(dir, entry, inst.Class); ok && w.holds(loc, stem, inst) {
			return loc, true
		}
	}
	return location{}, false
}

// an entry's name without its suffix, if it could hold an instance of class. a folder
// could, depending on its init
func entryStem(entry fs.DirEntry, class string) (string, bool) {
	switch {
	case entry.IsDir():
		return entry.Name(), true
	case !entry.Type().IsRegular():
		return "", false
	case scriptSuffix(class) != "":
		name, found, ok := classify(entry.Name())
		return name, ok && found == class && !isInit(entry.Name())
	}
	name, found, ok := classifyInst(entry.Name())
	return name, ok && found == class && name != "init"
}

// where an instance of class is, given the entry holding it
func (w *writer) entryLocation(dir string, entry fs.DirEntry, class string) (location, bool) {
	full := path.Join(dir, entry.Name())
	isScript := scriptSuffix(class) != ""
	if entry.IsDir() {
		init, err := w.scan.findInit(full)
		if err != nil || initClass(init) != class {
			return location{}, false
		}
		loc := location{path: full, props: initProps(full, class), dir: true}
		if isScript {
			loc.file = path.Join(full, init)
		}
		return loc, true
	}
	if isScript {
		return location{path: full, file: full, props: scriptProps(full, class)}, true
	}
	return location{path: full, props: full}, true
}

// reports whether the file at loc is this instance's. there's no stored mapping, so this
// goes by its id, or its name if it has none. an id that's not in the tree means someone
// edited the file, so the name decides
func (w *writer) holds(loc location, stem string, inst protocol.Instance) bool {
	file := w.readFileMeta(loc, inst.Class)
	if file.ID == inst.ID {
		return true
	}
	if file.ID != "" {
		if _, inTree := w.tree.Get(file.ID); inTree {
			return false
		}
	}
	// its own tag, on a file that's been renamed but has no .inst.json yet
	if strings.HasSuffix(stem, tagFor(inst.ID)) {
		return true
	}
	name := file.Name
	if name == "" {
		name = decodeName(stem)
	}
	return name == inst.Name
}

// returns the folder an instance's children go in, creating it if needed. Foo.luau becomes
// Foo/init.luau the first time, and Lamp.Part.inst.json becomes Lamp/init.Part.inst.json
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
	suffix := leafSuffix(inst.Class)
	dir := strings.TrimSuffix(loc.path, suffix)
	// Foo/ may belong to a Folder or another script called Foo. merging them would change
	// what the scan reads, so this one gets its own folder
	if tag := tagFor(id); w.exists(dir) && !strings.HasSuffix(dir, tag) {
		dir += tag
	}
	init := path.Join(dir, "init"+suffix)
	if err := w.checkPath(dir); err != nil {
		return "", err
	}
	if err := w.checkPath(init); err != nil {
		return "", err
	}
	if err := w.checkPath(initProps(dir, inst.Class)); err != nil {
		return "", err
	}
	if err := w.checkFree(dir); err != nil {
		return "", err
	}
	if err := w.root.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	separate := w.hasSeparateProps(loc)
	if err := w.root.Rename(loc.path, init); err != nil {
		w.root.Remove(dir)
		return "", err
	}
	if separate {
		if err := w.root.Rename(loc.props, initProps(dir, inst.Class)); err != nil {
			w.root.Rename(init, loc.path)
			w.root.Remove(dir)
			return "", err
		}
	}
	moved := location{path: dir, props: initProps(dir, inst.Class), dir: true}
	if loc.file != "" {
		moved.file = init
	}
	// a tag means the name doesn't match anymore, so the file has to say what it is
	if strings.HasSuffix(dir, tagFor(id)) {
		if err := w.writeProps(inst, moved, w.writeFile); err != nil {
			return "", err
		}
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
