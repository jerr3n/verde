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
}
