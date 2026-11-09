// SPDX-License-Identifier: MPL-2.0

// Package protocol holds the JSON messages that the server and its clients send on the WebSocket.
// Each message is an Envelope. The Data field of the Envelope holds one of the other types.
package protocol

import "encoding/json"

// Ref is the ID of an instance. Each instance in the tree has a different Ref.
type Ref string

// Envelope holds each message on the WebSocket.
// Type names the message, for example "hello" or "patch".
// Seq is the number of the message from its sender. A reply such as Error or Located uses Seq to refer to the request.
type Envelope struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Hello is the first message from a client.
// The server replies with a Snapshot, or with an Error if it refuses the client.
type Hello struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Client          string `json:"client,omitempty"`
	PlaceID         int64  `json:"placeId"`
	// Role is RoleEditor for an editor. Role is empty for a Studio plugin.
	Role string `json:"role,omitempty"`
}

// RoleEditor identifies a client that is not in a place, for example the VS Code extension.
// The server does not check an editor against serve.placeIds.
// The server does not count an editor in Status.Plugins.
const RoleEditor = "editor"

// Status tells an editor how many Studio plugins are connected.
// The server sends Status to each editor when the editor connects and when the count changes.
// The server does not send Status to plugins.
type Status struct {
	Plugins int `json:"plugins"`
}

// Locate asks the server for the location of an instance on disk. The server replies with Located.
type Locate struct {
	ID Ref `json:"id"`
}

// Located is the reply to Locate. Seq is the Seq of the Locate message.
// Each path is relative to the project root and uses "/" as the separator.
type Located struct {
	Seq   int64 `json:"seq"`
	Found bool  `json:"found"`
	// Path is the file of the instance. If Dir is true, Path is the folder of the instance.
	Path string `json:"path,omitempty"`
	// File is the file that holds the Source property. File is set only for a script.
	File string `json:"file,omitempty"`
	// Props is the .inst.json file of the instance. Props is empty if the instance has no .inst.json file.
	Props string `json:"props,omitempty"`
	Dir   bool   `json:"dir,omitempty"`
}

// Snapshot is the reply to Hello. It holds the full state of the project.
// OnConnect tells the plugin which side wins when disk and Studio are different at connect time.
type Snapshot struct {
	Root      Ref        `json:"root"`
	OnConnect string     `json:"onConnect"`
	Ignore    Ignore     `json:"ignore"`
	Mounts    []Mount    `json:"mounts"`
	Instances []Instance `json:"instances"`
}

// Ignore lists the instances and classes that the plugin must not sync.
// Each entry in Instances is a "/"-separated path from the game, for example "Workspace/Camera".
type Ignore struct {
	Instances []string `json:"instances"`
	Classes   []string `json:"classes"`
}

// Mount is an instance that exists in the game before sync starts, for example ReplicatedStorage.
// The server does not create or remove a mount. Sync is false for an ancestor that only holds other mounts.
type Mount struct {
	ID Ref `json:"id"`
	// Path must encode as [], not as null. Thus Path must not be a nil slice.
	Path []string `json:"path"`
	Sync bool     `json:"sync"`
}

// Value is one property value and the name of its type.
type Value struct {
	Type string `json:"t"`
	// Value is empty when Type is "Nil".
	Value json.RawMessage `json:"v,omitempty"`
}

// Instance is the full description of one instance. Parent is empty for the root.
type Instance struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name"`
	Class  string           `json:"class"`
	Props  map[string]Value `json:"props,omitempty"`
}

// Update is a change to one instance. Each empty field stays the same.
// Props holds only the properties that change.
type Update struct {
	ID     Ref              `json:"id"`
	Parent Ref              `json:"parent,omitempty"`
	Name   string           `json:"name,omitempty"`
	Props  map[string]Value `json:"props,omitempty"`
}

// Patch is a set of changes to instances.
// The server and the plugin can both send a Patch. The plugin replies to a Patch with Ack.
type Patch struct {
	Added   []Instance `json:"added,omitempty"`
	Updated []Update   `json:"updated,omitempty"`
	Removed []Ref      `json:"removed,omitempty"`
}

// Empty reports whether the patch has no changes.
func (p Patch) Empty() bool {
	return len(p.Added)+len(p.Updated)+len(p.Removed) == 0
}

// Ack tells the sender that the receiver applied a Patch. Seq is the Seq of that Patch.
type Ack struct {
	Seq int64 `json:"seq"`
}

// Error tells a client that the server cannot do a request.
// Seq is the Seq of the request. Seq is 0 if the server cannot read the request.
type Error struct {
	Seq     int64  `json:"seq,omitempty"`
	Message string `json:"message"`
}
