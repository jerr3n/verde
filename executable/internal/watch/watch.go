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

const debounce = 100 * time.Millisecond

func addDirs(w *fsnotify.Watcher, cfg *config.Cfg, logger *zap.Logger) {
	for _, entry := range cfg.Sync {
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

func Run(ctx context.Context, cfg *config.Cfg, changed func(), logger *zap.Logger) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	// A folder can only be watched if it exists. Studio can create one later, and with
	// no watch on it, there'd be no event to notice it by.
	for _, entry := range cfg.Sync {
		if err := os.MkdirAll(filepath.Join(cfg.Root, entry.Path), 0o755); err != nil {
			logger.Warn("create sync folder", zap.String("path", entry.Path), zap.Error(err))
		}
	}
	addDirs(w, cfg, logger)
	changed() // catches edits made between project.load and now

	var quiet <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			// chmod-only events do not change file's contents
			if ev.Op&^fsnotify.Chmod != 0 {
				quiet = time.After(debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			// events may have been lost, rescan finds wtv they were
			logger.Warn("watcher", zap.Error(err))
			quiet = time.After(debounce)
		case <-quiet:
			quiet = nil
			// new folders need adding. has to come before rescan.
			addDirs(w, cfg, logger)
			changed()
		}

	}
}
