// SPDX-License-Identifier: MPL-2.0

// Package config reads and makes the project.yml file of a Verde project.
// See initTemplate for an example of the file.
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

const (
	// DefaultPath is the name of the config file in the working directory. Each command uses this file.
	DefaultPath = "project.yml"
	// CurrentVersion is the only config version that this build can read.
	CurrentVersion = 0

	// defaultAddress is the address when serve.address is empty.
	defaultAddress = "127.0.0.1"
)

// OnConnect tells which side wins when disk and Studio are different at connect time.
type OnConnect string

const (
	// OnConnectDisk makes the disk win. This is the default.
	OnConnectDisk OnConnect = "disk"
	// OnConnectStudio makes Studio win. If the disk has instances that Studio does not have,
	// the plugin asks the user first, because those files would be deleted.
	OnConnectStudio OnConnect = "studio"
	// OnConnectInteractive makes the plugin ask the user when disk and Studio are different.
	OnConnectInteractive OnConnect = "interactive"
)

// SyncInstance connects one instance in the game to one folder on disk.
type SyncInstance struct {
	// Instance is the "/"-separated path of the instance, for example "StarterPlayer/StarterPlayerScripts".
	Instance string `yaml:"instance"`
	// Path is the folder, relative to Cfg.Root. Load cleans Path.
	Path string `yaml:"path"`
}

// Cfg is the contents of a project.yml file. Get a Cfg from Load, so that the defaults are set and the values are valid.
type Cfg struct {
	// Root is the absolute path of the folder that holds the config file.
	Root string `yaml:"-"`

	Version int    `yaml:"version"`
	Name    string `yaml:"name"`
	Serve   struct {
		Address  string  `yaml:"address"`
		Port     int     `yaml:"port"`
		PlaceIds []int64 `yaml:"placeIds"`
	} `yaml:"serve"`
	Sync struct {
		Instances []SyncInstance `yaml:"instances"`
	} `yaml:"sync"`
	Ignore struct {
		Paths     []string `yaml:"paths"`
		Instances []string `yaml:"instances"`
		Classes   []string `yaml:"classes"`
	} `yaml:"ignore"`
	Conflicts struct {
		OnConnect OnConnect `yaml:"onConnect"`
	} `yaml:"conflicts"`
}

// Hook is not used yet.
type Hook struct {
	Name    string        `yaml:"name"`
	Match   []string      `yaml:"match"`
	Run     []string      `yaml:"run"`
	Timeout time.Duration `yaml:"timeout"`
}

// Addr returns the host:port for the server.
func (c *Cfg) Addr() string {
	return net.JoinHostPort(c.Serve.Address, strconv.Itoa(c.Serve.Port))
}

// Load reads the config file at path, sets the defaults, and checks the values.
// Load refuses unknown fields.
func Load(path string) (*Cfg, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var cfg Cfg
	dec := yaml.NewDecoder(file)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse %s: file is empty", path)
		}
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.Root, err = filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", path, err)
	}
	return &cfg, nil
}

// applyDefaults sets the value of each optional field that is empty.
func (c *Cfg) applyDefaults() {
	if c.Serve.Address == "" {
		c.Serve.Address = defaultAddress
	}
	if c.Conflicts.OnConnect == "" {
		c.Conflicts.OnConnect = OnConnectDisk
	}
}

// validate checks the config. It also cleans each sync path.
func (c *Cfg) validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("version: unsupported version %d (expected %d)", c.Version, CurrentVersion)
	}
	if c.Serve.Port < 1 || c.Serve.Port > 65535 {
		return fmt.Errorf("serve.port: %d is not between 1 and 65535", c.Serve.Port)
	}
	switch c.Conflicts.OnConnect {
	case OnConnectDisk, OnConnectStudio, OnConnectInteractive:
	default:
		return fmt.Errorf("conflicts.onConnect: %q is not disk, studio or interactive", c.Conflicts.OnConnect)
	}
	if len(c.Sync.Instances) == 0 {
		return errors.New("sync.instances: need at least one entry")
	}

	for i := range c.Sync.Instances {
		entry := &c.Sync.Instances[i]

		if err := validateInstance(entry.Instance); err != nil {
			return fmt.Errorf("sync.instances[%d].instance: %w", i, err)
		}
		cleaned, err := cleanPath(entry.Path)
		if err != nil {
			return fmt.Errorf("sync.instances[%d].path: %w", i, err)
		}
		entry.Path = cleaned

		for j, earlier := range c.Sync.Instances[:i] {
			if err := checkOverlap(entry.Instance, earlier.Instance, "/"); err != nil {
				return fmt.Errorf("sync.instances[%d].instance: %w with sync.instances[%d]", i, err, j)
			}
			if err := checkOverlap(entry.Path, earlier.Path, string(filepath.Separator)); err != nil {
				return fmt.Errorf("sync.instances[%d].path: %w with sync.instances[%d]", i, err, j)
			}
		}
	}
	return nil
}

// validateInstance checks that instance has no empty part between the "/" separators.
func validateInstance(instance string) error {
	if slices.Contains(strings.Split(instance, "/"), "") {
		return fmt.Errorf("%q is not a /-separated instance path", instance)
	}
	return nil
}

// cleanPath cleans a sync path, and checks that the path is a folder inside the project.
// The check uses only the text of the path. It does not follow symlinks.
func cleanPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("is empty")
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsLocal(cleaned) {
		return "", fmt.Errorf("%q is outside the project", path)
	}
	if cleaned == "." {
		return "", fmt.Errorf("%q is the project root, not a folder inside it", path)
	}
	return cleaned, nil
}

// checkOverlap refuses two sync targets that are the same, or where one target is inside the other.
// Without this check, Verde syncs some files two times.
// Also, the plugin sees the inner mount as an instance that is only in Studio.
func checkOverlap(a, b, separator string) error {
	switch {
	case a == b:
		return fmt.Errorf("%q is a duplicate", a)
	case strings.HasPrefix(a, b+separator), strings.HasPrefix(b, a+separator):
		return fmt.Errorf("%q and %q overlap", a, b)
	}
	return nil
}
