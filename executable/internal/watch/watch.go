// SPDX-License-Identifier: MPL-2.0

package watch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jerr3n/verde/executable/internal/config"
	"go.uber.org/zap"
)

// debounce is how long the disk must stay quiet before Run calls changed.
// One save in an editor can cause many events, and the delay groups them into one rescan.
const debounce = 100 * time.Millisecond

// addDirs adds a watch on each folder in each sync folder tree.
// fsnotify does not watch subfolders, so each folder needs its own watch.
// addDirs logs a folder that it cannot watch and continues with the other folders.
func addDirs(w *fsnotify.Watcher, cfg *config.Cfg, logger *zap.Logger) {
	for _, entry := range cfg.Sync.Instances {
		root := filepath.Join(cfg.Root, entry.Path)
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if err := w.Add(path); err != nil {
					logger.Warn("watch", zap.String("path", path), zap.Error(err))
				}
			}
			return nil
		})
	}
}

// Run watches the sync folders and calls changed after each burst of disk events.
// Run also calls changed one time at the start.
// Run returns nil when ctx is done or when the watcher closes.
func Run(ctx context.Context, cfg *config.Cfg, changed func(), logger *zap.Logger) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	// fsnotify can watch only a folder that exists.
	// If a sync folder is missing, Studio can make it later, and no event tells Run about it.
	// Thus Run makes each sync folder before it adds the watches.
	for _, entry := range cfg.Sync.Instances {
		if err := os.MkdirAll(filepath.Join(cfg.Root, entry.Path), 0o755); err != nil {
			logger.Warn("create sync folder", zap.String("path", entry.Path), zap.Error(err))
		}
	}
	addDirs(w, cfg, logger)
	// This call finds the edits that occurred between project.Load and the start of the watch.
	changed()

	var quiet <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			// An event that is only a chmod does not change the contents of a file.
			if ev.Op&^fsnotify.Chmod != 0 {
				quiet = time.After(debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			// After an error, some events can be lost. A rescan finds the changes that they had.
			logger.Warn("watcher", zap.Error(err))
			quiet = time.After(debounce)
		case <-quiet:
			quiet = nil
			// Add watches on new folders before the rescan.
			// Then a change in a new folder after the rescan causes an event.
			addDirs(w, cfg, logger)
			changed()
		}

	}
}
