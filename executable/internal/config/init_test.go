package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestInitWritesAValidConfig(t *testing.T) {
	// The name has characters that would break the YAML if it weren't encoded.
	dir := filepath.Join(t.TempDir(), "my: game #1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DefaultPath)

	if err := Init(path); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Name, "my: game #1"; got != want {
		t.Errorf("Name = %q, want %q", got, want)
	}
	if got, want := cfg.Addr(), "127.0.0.1:45678"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if got := cfg.Serve.PlaceIds; len(got) != 0 {
		t.Errorf("PlaceIds = %v, want none, so any place can connect", got)
	}
	if got, want := len(cfg.Sync), 8; got != want {
		t.Errorf("len(Sync) = %d, want %d", got, want)
	}
	for _, entry := range cfg.Sync {
		info, err := os.Stat(filepath.Join(dir, entry.Path))
		if err != nil {
			t.Errorf("sync folder: %v", err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a folder", entry.Path)
		}
	}
}

func TestInitKeepsExistingFolders(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "src", "shared", "Hello.luau")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Init(filepath.Join(dir, DefaultPath)); err != nil {
		t.Fatalf("Init: %v", err)
	}

	got, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "return 1\n" {
		t.Errorf("script changed to %q", got)
	}
}

func TestInitRemovesConfigIfAFolderFails(t *testing.T) {
	dir := t.TempDir()
	// A file called src stops any of the sync folders from being created.
	blocker := filepath.Join(dir, "src")
	if err := os.WriteFile(blocker, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DefaultPath)

	if err := Init(path); err == nil {
		t.Fatal("Init succeeded, want an error")
	}

	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("config still exists: %v", err)
	}
	got, err := os.ReadFile(blocker)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "mine" {
		t.Errorf("src changed to %q", got)
	}
}

func TestInitStaysInsideTheProject(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "src")); err != nil {
		t.Skipf("can't create symlinks: %v", err)
	}

	if err := Init(filepath.Join(dir, DefaultPath)); err == nil {
		t.Fatal("Init succeeded, want an error")
	}

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("created %d entries outside the project", len(entries))
	}
}

func TestInitKeepsAnExistingFile(t *testing.T) {
	const existing = "name: mine\n"
	path := writeConfig(t, existing)

	if err := Init(path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Init error = %v, want fs.ErrExist", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Errorf("file changed to %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "src")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("src was created: %v", err)
	}
}
