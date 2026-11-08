// SPDX-License-Identifier: MPL-2.0

package protocol

import "encoding/json"

// Ref unique id of an instance
type Ref string

// Envelope wrapper around every message
// contains `Type`, `Seq`, and `Data`
type Envelope struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Hello plugin's first message
type Hello struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Client          string `json:"client,omitempty"`
	PlaceID         int64  `json:"placeId"`
	Role            string `json:"role,omitempty"` // RoleEditor, or empty for a Studio plugin
}

// RoleEditor marks a client that isn't in a place, like the VS Code extension. placeIds
// don't apply to it, and it isn't counted as a connected Studio.
const RoleEditor = "editor"

// Status what's connected, sent to editors when they connect and when it changes
type Status struct {
	Plugins int `json:"plugins"`
}

// Locate asks where an instance is on disk
type Locate struct {
	ID Ref `json:"id"`
}

// Located answers a Locate. Paths are /-separated from the project root
type Located struct {
	Seq   int64  `json:"seq"`
	Found bool   `json:"found"`
	Path  string `json:"path,omitempty"`  // its file, or its folder if Dir
	File  string `json:"file,omitempty"`  // file holding Source, if it's a script
	Props string `json:"props,omitempty"` // its .inst.json, if it has one
	Dir   bool   `json:"dir,omitempty"`
}

// Snapshot server's reply, full project state
type Snapshot struct {
	Root      Ref        `json:"root"`
	OnConnect string     `json:"onConnect"`
	Ignore    Ignore     `json:"ignore"`
	Mounts    []Mount    `json:"mounts"`
	Instances []Instance `json:"instances"`
}

// Ignore what  should be ignored
type Ignore struct {
	Instances []string `json:"instances"`
	Classes   []string `json:"classes"`
}

// Mount instance that exists in game already
type Mount struct {
	ID   Ref      `json:"id"`
	Path []string `json:"path"` // must encode as [], not null
	Sync bool     `json:"sync"`
}

// Value one property val, w/ type
type Value struct {
	Type  string          `json:"t"`
	Value json.RawMessage `json:"v,omitempty"` // Nil has no v
}

// Instance description of 1 Instance
type Instance struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name"`
	Class  string           `json:"class"`
	Props  map[string]Value `json:"props,omitempty"`
}

// Update a partial change to one instance
type Update struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name,omitempty"`
	Props  map[string]Value `json:"props,omitempty"`
}

// Patch a batch of changes, can be sent in either dir
type Patch struct {
	Added   []Instance `json:"added,omitempty"`
	Updated []Update   `json:"updated,omitempty"`
	Removed []Ref      `json:"removed,omitempty"`
}

// Empty reports whether a patch changes nothing
func (p Patch) Empty() bool {
	return len(p.Added)+len(p.Updated)+len(p.Removed) == 0
}

// Ack acknowledge
type Ack struct {
	Seq int64 `json:"seq"`
}

// Error guesss
type Error struct {
	Seq     int64  `json:"seq,omitempty"`
	Message string `json:"message"`
}
