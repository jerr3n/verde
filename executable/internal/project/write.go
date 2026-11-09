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

// trashDir holds the files that Studio deleted. trashDir is outside each sync folder, so a scan never reads it.
const trashDir = ".verde/trash"

// blocked is the reason that an instance cannot have a file.
// A failed write can succeed if Verde tries it again. A blocked instance cannot get a file until a different thing changes.
// For example, the sibling that uses its name must go away first.
type blocked string

// Error returns the reason as text.
func (b blocked) Error() string { return string(b) }

// errParentNotOnDisk blocks an instance whose parent has no file or folder.
const errParentNotOnDisk = blocked("its parent isn't on disk")

// Apply applies a patch from Studio to the tree, and then writes the changes to the files.
// It returns the part of the patch that it applied, so that the caller can send it to other clients.
// It also returns an error for each change that failed.
//
// One bad change does not stop the other changes, because the plugin already made all of the changes and does not undo them.
// If an instance cannot get a file, the instance stays in the tree and becomes fileless.
// Thus the next scan does not see the missing file as a delete.
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

	// Report the fileless instances only at the end, because a later change in the patch can remove the problem.
	// For example, a swap of two names is not a problem. A delete of a script followed by the add of its replacement is also not a problem.
	for _, f := range w.newlyFileless {
		if t.IsFileless(f.id) {
			w.report(f.id, fmt.Errorf("not saved to disk: %w", f.err))
		}
	}
	return w.applied, w.errs
}

// Locate finds the files of an instance. Found is false if the instance has no files.
// Two examples are a mount that only holds a sync target, and a fileless instance.
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

// newWriter makes a writer for one Apply or Locate.
func newWriter(cfg *config.Cfg, t *tree.Tree, mounts []protocol.Mount, root *os.Root) *writer {
	w := &writer{
		tree:     t,
		root:     root,
		scan:     &loader{cfg: cfg, files: root.FS()},
		mounts:   make(map[protocol.Ref]bool, len(mounts)),
		syncDirs: make(map[protocol.Ref]string, len(cfg.Sync.Instances)),
	}
	for _, mount := range mounts {
		w.mounts[mount.ID] = true
		if !mount.Sync {
			continue
		}
		for _, entry := range cfg.Sync.Instances {
			if entry.Instance == strings.Join(mount.Path, "/") {
				w.syncDirs[mount.ID] = filepath.ToSlash(entry.Path)
			}
		}
	}
	return w
}

// writer holds the state of one Apply or Locate.
type writer struct {
	tree *tree.Tree
	root *os.Root
	// scan gives the writer the same rules for init files and ignores that Load uses.
	scan   *loader
	mounts map[protocol.Ref]bool
	// syncDirs maps each sync target to its folder.
	syncDirs map[protocol.Ref]string

	// applied is the part of the patch that Apply applied.
	applied protocol.Patch
	errs    []error
	// newlyFileless holds each instance that became fileless during this patch, and the reason.
	newlyFileless []filelessReason
	// tagged holds each instance that got a tag during this patch, because a sibling used its name.
	tagged []protocol.Ref
}

// filelessReason is an instance and the error that made it fileless.
type filelessReason struct {
	id  protocol.Ref
	err error
}

// location is the place of an instance on disk.
// Each path is relative to the project root and uses "/" as the separator.
type location struct {
	// path is the file of the instance. If dir is true, path is the folder of the instance.
	path string
	// file is the file that holds Source. file is "" if the instance is not a script.
	file string
	// props is the .inst.json file of the instance. A script or a folder can have no .inst.json file.
	props string
	// dir is true when path is a folder that holds the children of the instance.
	dir bool
}

// report adds err to the errors, with the instance path of id as a prefix.
func (w *writer) report(id protocol.Ref, err error) {
	w.errs = append(w.errs, fmt.Errorf("%s: %w", w.instancePath(id), err))
}

// instancePath returns the path of an instance without "game/", in the same form as the paths in the config.
// ignore.instances uses this form too.
func (w *writer) instancePath(id protocol.Ref) string {
	var names []string
	for {
		inst, ok := w.tree.Get(id)
		if !ok {
			// Put an unknown ref in the path, so that the error message shows it.
			names = append(names, string(id))
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

// insideSync reports whether id is a sync target or is inside a sync target.
// The plugin works only inside sync targets. A change at a different location is a bug, and it must not change files.
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

// checkSource removes a Source property that is not a String, because Verde cannot write it to a file.
func (w *writer) checkSource(id protocol.Ref, class string, props map[string]protocol.Value) {
	if value, ok := props["Source"]; ok && scriptSuffix(class) != "" && value.Type != "String" {
		delete(props, "Source")
		w.report(id, fmt.Errorf("Source must be String, not %s", value.Type))
	}
}

// add puts a new instance from Studio in the tree and on disk.
func (w *writer) add(inst protocol.Instance) {
	if !w.insideSync(inst.Parent) {
		w.errs = append(w.errs, fmt.Errorf("%s: its parent isn't inside a sync target", inst.Name))
		return
	}

	w.checkSource(inst.ID, inst.Class, inst.Props)
	// Each script has a file, and thus has a Source. Without this step, the next scan
	// finds an empty Source that is not in the tree, and sends it to Studio.
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

// update applies a change from Studio to the tree, and then moves or writes the files.
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

	// Do this before the tree changes, because locate uses the old names.
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
			// The file did not move. Put the old name and parent back in the tree.
			// Then the next scan matches the file to the instance. It does not see a new instance and a delete.
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
	// After a move, a .inst.json file from a deleted script can be at the new location. Write the props again.
	if renamed || moved || changesSavedProps(before.Class, u.Props) {
		w.saveProps(u.ID)
	}

	if u.Name != "" || u.Parent != "" || len(u.Props) > 0 {
		w.applied.Updated = append(w.applied.Updated, u)
	}
}

// changesSavedProps reports whether props changes a property other than the Source of a script.
func changesSavedProps(class string, props map[string]protocol.Value) bool {
	for name := range props {
		if name != "Source" || scriptSuffix(class) == "" {
			return true
		}
	}
	return false
}

// relocate moves the file or folder of an instance to the location that the tree now gives.
// relocate renames a folder as one unit. Thus files that are not instances, such as a README, move with the folder.
// If relocate returns an error, nothing moved.
// If the instance is blocked at the new location, relocate moves the old files to the trash and makes the instance fileless.
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

// hasSeparateProps reports whether a script stored as one file has a .inst.json file next to it.
// That .inst.json file must move with the script.
// A script stored as a folder keeps its .inst.json file in the folder, so the file moves with the folder.
func (w *writer) hasSeparateProps(loc location) bool {
	if loc.dir || loc.file == "" {
		return false
	}
	_, err := w.root.Lstat(loc.props)
	return err == nil
}

// trashLocation moves the file or folder at loc to the trash. It also moves a separate .inst.json file.
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

// relocateTarget returns the new path for an instance that moves from old.
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

// chooseTarget returns the path for the file of an instance in dir. If asDir is true, it returns the path for the folder.
//   - If no sibling uses the encoded name, chooseTarget returns that name.
//   - If a sibling uses the encoded name, chooseTarget adds the tag. The .inst.json file then holds the real name and the ref.
//
// current is the present path of the instance. chooseTarget does not count current as used.
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
	// On a case-insensitive file system, a rename that only changes the case finds the old file.
	// That file is not a different sibling, so do not count it.
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
	// The .inst.json file holds the real name, so the scan must be able to read that file.
	if err := w.checkPath(propsFileFor(tagged, inst.Class, asDir)); err != nil {
		return "", err
	}
	if err := w.checkFree(tagged); err != nil {
		return "", err
	}
	w.tagged = append(w.tagged, inst.ID)
	return tagged, nil
}

// propsFileFor returns the path of the .inst.json file for an instance stored at target.
func propsFileFor(target, class string, asDir bool) string {
	switch {
	case asDir:
		return initProps(target, class)
	case scriptSuffix(class) != "":
		return scriptProps(target, class)
	}
	return target
}

// takenInOtherCase reports whether a sibling of the same class uses name with a different case, or uses name as a folder.
// Lstat does not find these siblings.
// This check is necessary because Foo and foo are the same file on Windows and macOS.
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

// dropUnneededTags moves each instance that got a tag during this patch back to its plain name, if the name is free now.
// For example, a swap of two names needs a tag only in the middle of the patch. A replacement of a script is the same.
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

// remove applies the removals in a patch.
// The plugin also lists the descendants, in no specified order.
// Thus remove changes the disk only for the highest removed instance in each subtree.
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
		// The file stays, so the next scan adds the instance again. This reverses the delete from Studio.
		// No data is lost, and this error tells the user the reason.
		if err := w.trashLocation(loc); err != nil {
			w.errs = append(w.errs, fmt.Errorf("remove %s: %w", loc.path, err))
		}
	}
}

// retryFileless tries again to put each fileless instance on disk, because this patch may have removed the problem.
// It does not report the errors again, because Verde already reported each one.
func (w *writer) retryFileless() {
	for _, id := range w.tree.Fileless() {
		// If the parent was placed first, this instance can already be on disk.
		if w.tree.IsFileless(id) {
			w.place(id)
		}
	}
}

// place puts an instance and its descendants on disk. If that fails, place makes the instance fileless.
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

// markFileless makes id fileless, and records err for the report at the end of Apply.
func (w *writer) markFileless(id protocol.Ref, err error) {
	// Do not record errParentNotOnDisk, because the error of the parent already tells the reason.
	// Do not record an instance that was already fileless, so that a retry does not report the same error again.
	if !w.tree.IsFileless(id) && !errors.Is(err, errParentNotOnDisk) {
		w.newlyFileless = append(w.newlyFileless, filelessReason{id, err})
	}
	w.tree.SetFileless(id, true)
}

// createOnDisk writes the file or folder for a new instance.
func (w *writer) createOnDisk(inst protocol.Instance) error {
	if err := w.checkAllowed(inst); err != nil {
		return err
	}
	dir, err := w.childDir(inst.Parent)
	if err != nil {
		return err
	}

	// A file can already be at this location. An earlier write can have failed, or a person made the file before the scan.
	// The tree wins, and replaceFile moves the old text to the trash.
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

// claimedBySibling reports whether a file without a stored ref belongs to a sibling of the new instance inst.
// The sibling has the same name and class.
// If the file belongs to the sibling, claimedBySibling writes the ref of the sibling into the file.
// Thus a later scan can make the two instances different.
// A file without a stored ref always belongs to the only instance with its name and class.
func (w *writer) claimedBySibling(dir string, inst protocol.Instance, loc location) (bool, error) {
	if w.readFileMeta(loc, inst.Class).ID != "" {
		return false, nil
	}
	for _, id := range w.tree.Children(inst.Parent) {
		other, _ := w.tree.Get(id)
		if id == inst.ID || other.Class != inst.Class || other.Name != inst.Name || w.tree.IsFileless(id) {
			continue
		}
		// The sibling has its own tagged file, so this file does not belong to it.
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

// exists reports whether name exists. It does not follow a symlink.
func (w *writer) exists(name string) bool {
	_, err := w.root.Lstat(name)
	return err == nil
}

// readFileMeta returns the contents of the .inst.json file at loc. It returns an empty instFile if it cannot read the file.
func (w *writer) readFileMeta(loc location, class string) instFile {
	file, err := w.scan.readOptionalInst(loc.props, class)
	if err != nil {
		return instFile{}
	}
	return file
}

// replaceFile writes text to name. If name already holds different text, replaceFile first moves the old file to the trash.
func (w *writer) replaceFile(name, text string) error {
	if current, err := w.root.ReadFile(name); err == nil && string(current) != text {
		if err := w.trash(name); err != nil {
			return err
		}
	}
	return w.writeFile(name, text)
}

// saveProps writes the properties of an instance to its .inst.json file.
// If the write fails, saveProps makes the instance fileless. Without this step, the next scan sends the old values back over the change.
// saveProps skips mounts, because Verde does not sync the properties of services.
func (w *writer) saveProps(id protocol.Ref) {
	if w.mounts[id] || w.tree.IsFileless(id) {
		return
	}
	loc, onDisk := w.locate(id)
	if !onDisk {
		// The file is gone, so place makes a new file.
		w.place(id)
		return
	}
	inst, _ := w.tree.Get(id)
	if err := w.writeProps(inst, loc, w.writeFile); err != nil {
		w.markFileless(id, err)
	}
}

// writeProps writes the .inst.json file at loc with write.
//   - A script or a Folder gets a .inst.json file only if the file has data to store.
//   - A script or a Folder has no .inst.json file if the path matches ignore.paths. Its other file or folder shows that it exists.
//   - Each other instance always gets a .inst.json file, because that file is the only file of the instance.
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
		// This instance has the plain name, but a sibling has the same name and class.
		// Keep the stored ref, so that the scan can make the siblings different.
		file.ID = inst.ID
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
			// If the old file stays, the next scan sends its values to Studio.
			return w.trash(loc.props)
		}
	}
	text, err := encodeInst(file)
	if err != nil {
		return err
	}
	return write(loc.props, text)
}

// hasTwin reports whether a sibling of inst has the same name and class.
func (w *writer) hasTwin(inst protocol.Instance) bool {
	for _, id := range w.tree.Children(inst.Parent) {
		other, _ := w.tree.Get(id)
		if id != inst.ID && other.Class == inst.Class && other.Name == inst.Name {
			return true
		}
	}
	return false
}

// writeSource writes the Source of an instance to its file.
// If the write fails, writeSource makes the instance fileless. Without this step, the next scan sends the old text back over the edit.
func (w *writer) writeSource(id protocol.Ref) {
	if w.tree.IsFileless(id) {
		// retryFileless writes the Source with the other fileless instances.
		return
	}
	loc, onDisk := w.locate(id)
	if !onDisk {
		// The file is gone, so place makes a new file.
		w.place(id)
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

// checkAllowed checks the rules that do not depend on the location of the file.
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

// checkPath returns a blocked error if target matches ignore.paths.
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

// checkFree returns a blocked error if target already exists.
func (w *writer) checkFree(target string) error {
	if _, err := w.root.Lstat(target); err == nil {
		return blocked(target + " already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// locate finds an instance on disk from the tree and from the contents of each folder.
// Verde does not store a map from instances to files, because such a map becomes wrong when a person changes the files.
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

// find looks in dir for the file of an instance, by its tag or by its name.
// find decodes each entry. It does not check only the encoded path,
// because a file from outside Verde can hold a character that encodeName encodes, for example What?.luau.
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
		// A file with the tag of the instance wins over a file with its name, because the named file can belong to a sibling.
		if tagged {
			return loc, true
		}
		named, foundNamed = loc, true
	}
	return named, foundNamed
}

// search looks in dir for a file that a person renamed, so that its name does not identify the instance.
// search opens each file of the class. Thus use it only when find fails and the instance must have a file.
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

// entryStem returns the name of entry without its suffix. ok is false if entry cannot hold an instance of class.
// A folder can hold any class, because its init file sets the class.
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

// entryLocation returns the location of an instance of class that is stored in entry. ok is false if a folder has a different class.
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

// holds reports whether the file at loc belongs to inst.
// Verde does not store a map from instances to files. Thus holds uses the stored ref, or the name if there is no stored ref.
// If the stored ref is not in the tree, a person edited the file. Then the name decides.
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
	// The file has the tag of the instance, but has no .inst.json file yet.
	if strings.HasSuffix(stem, tagFor(inst.ID)) {
		return true
	}
	name := file.Name
	if name == "" {
		name = decodeName(stem)
	}
	return name == inst.Name
}

// childDir returns the folder for the children of an instance. If the folder does not exist, childDir makes it.
// For the first child, Foo.luau becomes Foo/init.luau, and Lamp.Part.inst.json becomes Lamp/init.Part.inst.json.
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
	// Foo/ can belong to a Folder or to a different script with the name Foo.
	// A merge of the two folders changes what the scan reads, so this instance gets its own folder.
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
	// With a tag, the folder name is not the instance name. The .inst.json file must hold the name and the ref.
	if strings.HasSuffix(dir, tagFor(id)) {
		if err := w.writeProps(inst, moved, w.writeFile); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// writeFile replaces the contents of a file in one step. It writes a temporary file and then renames it.
// Thus a crash cannot leave a script that is only half written.
//   - If the text is the same, writeFile does not touch the file. Thus editors do not see a change.
//   - The temporary name is not a script name. Thus a scan skips a temporary file that stays after a crash.
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

// trash moves a file or folder into the trash. It does not delete it,
// because a delete from Studio can destroy work that is not in a commit.
func (w *writer) trash(name string) error {
	if err := w.root.MkdirAll(trashDir, 0o755); err != nil {
		return err
	}
	// This file keeps the deleted scripts out of git. The project does not need to change its own .gitignore.
	w.root.WriteFile(path.Join(trashDir, ".gitignore"), []byte("*\n"), 0o644)

	// Each call gets its own folder. Thus the same path can go to the trash more than one time.
	target := path.Join(trashDir, time.Now().UTC().Format("20060102-150405.000000000"), name)
	if err := w.root.MkdirAll(path.Dir(target), 0o755); err != nil {
		return err
	}
	return w.root.Rename(name, target)
}

// sourceText returns the Source of a script as text. It returns "" if the instance has no Source.
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
