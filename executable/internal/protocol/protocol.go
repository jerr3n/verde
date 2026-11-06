package protocol

import "encoding/json"

// unique id of an instance
type Ref string

// wrapper around every message
// contains `Type`, `Seq`, and `Data`
type Envelope struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data,omitempty"`
}

// plugin's first message
type Hello struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Client          string `json:"client,omitempty"`
	PlaceID         int64  `json:"placeId"`
}

// server's reply, full project state
type Snapshot struct {
	Root      Ref        `json:"root"`
	OnConnect string     `json:"onConnect"`
	Ignore    Ignore     `json:"ignore"`
	Mounts    []Mount    `json:"mounts"`
	Instances []Instance `json:"instances"`
}

// what  should be ignored
type Ignore struct {
	Instances []string `json:"instances"`
	Classes   []string `json:"classes"`
}

// instance that exists in game already
type Mount struct {
	ID   Ref      `json:"id"`
	Path []string `json:"path"` // must encode as [], not null
	Sync bool     `json:"sync"`
}

// one property val, w/ type
type Value struct {
	Type  string          `json:"t"`
	Value json.RawMessage `json:"v,omitempty"` // Nil has no v
}

// description of 1 Instance
type Instance struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name"`
	Class  string           `json:"class"`
	Props  map[string]Value `json:"props,omitempty"`
}

// a partial change to one instance
type Update struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name,omitempty"`
	Props  map[string]Value `json:"props,omitempty"`
}

// a batch of changes, can be sent in either dir
type Patch struct {
	Added   []Instance `json:"added,omitempty"`
	Updated []Update   `json:"updated,omitempty"`
	Removed []Ref      `json:"removed,omitempty"`
}

// acknowledge
type Ack struct {
	Seq int64 `json:"seq"`
}

// guesss
type Error struct {
	Seq     int64  `json:"seq,omitempty"`
	Message string `json:"message"`
}
