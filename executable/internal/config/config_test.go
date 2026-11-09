// SPDX-License-Identifier: MPL-2.0

package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validSync = `
sync:
  instances:
    - instance: Workspace
      path: src/workspace
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultPath)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSample(t *testing.T) {
	path := filepath.Join("..", "..", "test", "project.yml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got, want := cfg.Addr(), "127.0.0.1:45678"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if got, want := cfg.Conflicts.OnConnect, OnConnectDisk; got != want {
		t.Errorf("OnConnect = %q, want %q", got, want)
	}
	if got, want := len(cfg.Sync.Instances), 8; got != want {
		t.Errorf("len(Sync) = %d, want %d", got, want)
	}
	if got, want := cfg.Serve.PlaceIds, int64(1234567890); len(got) != 1 || got[0] != want {
		t.Errorf("PlaceIds = %v, want [%d]", got, want)
	}
	if got, want := cfg.Ignore.Instances, "Workspace/Camera"; len(got) != 2 || got[0] != want {
		t.Errorf("Ignore.Instances = %v, want it to start with %q", got, want)
	}

	wantRoot, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Root != wantRoot {
		t.Errorf("Root = %q, want %q", cfg.Root, wantRoot)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, "serve:\n  port: 1234\n"+validSync))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Addr(), "127.0.0.1:1234"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if got, want := cfg.Conflicts.OnConnect, OnConnectDisk; got != want {
		t.Errorf("OnConnect = %q, want %q", got, want)
	}
}

func TestLoadCleansPaths(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
serve: {port: 1234}
sync:
  instances:
    - {instance: Workspace, path: ./src/a/../workspace/}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Sync.Instances[0].Path, filepath.Join("src", "workspace"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestLoadAllowsSimilarNames(t *testing.T) {
	_, err := Load(writeConfig(t, `
serve: {port: 1234}
sync:
  instances:
    - {instance: StarterPlayer/A, path: src/a}
    - {instance: StarterPlayer/AB, path: src/ab}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadAcceptsEachOnConnect(t *testing.T) {
	for _, value := range []OnConnect{OnConnectDisk, OnConnectStudio, OnConnectInteractive, OnConnectBidirectional} {
		t.Run(string(value), func(t *testing.T) {
			cfg, err := Load(writeConfig(t, "serve: {port: 1234}\nconflicts: {onConnect: "+string(value)+"}\n"+validSync))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Conflicts.OnConnect != value {
				t.Errorf("OnConnect = %q, want %q", cfg.Conflicts.OnConnect, value)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), DefaultPath))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadRejects(t *testing.T) {
	const port = "serve: {port: 1234}\n"

	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty file", "", "file is empty"},
		{"only comments", "# nothing here\n", "file is empty"},
		{"not yaml", "serve: [", "parse"},
		{"unknown field", "serve:\n  prot: 1234\n" + validSync, "prot"},
		{"misplaced field", port + "onConnect: disk\n" + validSync, "onConnect"},
		{"unsupported version", "version: 99\n" + port + validSync, "version"},
		{"missing port", validSync, "serve.port"},
		{"port too low", "serve: {port: -1}\n" + validSync, "serve.port"},
		{"port too high", "serve: {port: 70000}\n" + validSync, "serve.port"},
		{"bad onConnect", port + "conflicts: {onConnect: banana}\n" + validSync, "conflicts.onConnect"},
		{"no sync", port, "sync.instances: need at least one"},
		{"empty instance", port + "sync:\n  instances:\n    - {path: src/a}\n", "sync.instances[0].instance"},
		{"instance with empty segment", port + "sync:\n  instances:\n    - {instance: A//B, path: src/a}\n", "sync.instances[0].instance"},
		{"instance with trailing slash", port + "sync:\n  instances:\n    - {instance: A/, path: src/a}\n", "sync.instances[0].instance"},
		{"empty path", port + "sync:\n  instances:\n    - {instance: A}\n", "sync.instances[0].path"},
		{"path outside project", port + "sync:\n  instances:\n    - {instance: A, path: ../../outside}\n", "outside the project"},
		{"path sneaking outside", port + "sync:\n  instances:\n    - {instance: A, path: src/../../outside}\n", "outside the project"},
		{"absolute path", port + "sync:\n  instances:\n    - {instance: A, path: /etc}\n", "outside the project"},
		{"project root", port + "sync:\n  instances:\n    - {instance: A, path: ./}\n", "project root"},
		{"duplicate instance", port + "sync:\n  instances:\n    - {instance: A, path: x}\n    - {instance: A, path: y}\n", "duplicate"},
		{"duplicate path", port + "sync:\n  instances:\n    - {instance: A, path: x}\n    - {instance: B, path: x}\n", "duplicate"},
		{"duplicate path spelled differently", port + "sync:\n  instances:\n    - {instance: A, path: x}\n    - {instance: B, path: ./x/}\n", "duplicate"},
		{"nested instance", port + "sync:\n  instances:\n    - {instance: A, path: x}\n    - {instance: A/B, path: y}\n", "overlap"},
		{"nested instance, inner first", port + "sync:\n  instances:\n    - {instance: A/B, path: x}\n    - {instance: A, path: y}\n", "overlap"},
		{"nested path", port + "sync:\n  instances:\n    - {instance: A, path: src}\n    - {instance: B, path: src/x}\n", "overlap"},
		{"nested path, inner first", port + "sync:\n  instances:\n    - {instance: A, path: src/x}\n    - {instance: B, path: src}\n", "overlap"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, tt.body))
			if err == nil {
				t.Fatalf("Load succeeded with %+v, want an error containing %q", cfg, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}
