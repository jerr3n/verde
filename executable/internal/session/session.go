// SPDX-License-Identifier: MPL-2.0

// Package session connects the WebSocket clients to the project tree and to the disk.
// One goroutine, Run, owns the tree and the client list. Other goroutines send events to Run.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/project"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/tree"
	"go.uber.org/zap"
)

// protocolVersion is the version that a client must send in Hello.
const protocolVersion = 1

// client is one WebSocket connection, from a Studio plugin or from an editor.
type client struct {
	conn *websocket.Conn
	// queue holds the messages that writeLoop did not send yet.
	queue chan protocol.Envelope
	// seq is the Seq of the last message that writeLoop sent. Only writeLoop uses seq.
	seq int64

	// editor is true when the Hello had the role protocol.RoleEditor. Only Run uses editor.
	editor bool
}

// eventKind is the type of an event.
type eventKind int

const (
	eventMessage eventKind = iota
	eventDisconnect
	EventDiskChanged
)

// event is a request to Run from a connection or from the disk watcher.
type event struct {
	kind   eventKind
	client *client
	// data is the raw message. data is set only for eventMessage.
	data []byte
}

// session is the state that Run owns. Make a session with New.
type session struct {
	cfg    *config.Cfg
	t      *tree.Tree
	mounts []protocol.Mount
	logger *zap.Logger

	// clients holds each client that completed Hello. Only Run uses clients.
	clients map[*client]struct{}
	// events carries the events from the connections and the watcher to Run.
	events chan event
	// done closes when Run returns.
	done chan struct{}
}

// New makes a session for the project tree t. Start Run before you call Handle or DiskChanged.
func New(cfg *config.Cfg, t *tree.Tree, mounts []protocol.Mount, logger *zap.Logger) *session {
	return &session{
		cfg:    cfg,
		t:      t,
		mounts: mounts,
		logger: logger,

		clients: make(map[*client]struct{}),
		events:  make(chan event),
		done:    make(chan struct{}),
	}
}

// send puts a message in the queue of c. send does not wait.
// If the queue is full, send closes the connection and returns an error.
// A slow client thus cannot stop Run.
func (c *client) send(msgType string, data any) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}

	e := protocol.Envelope{Type: msgType, Data: encoded}
	select {
	case c.queue <- e:
		return nil
	default:
		// The client cannot get all of its messages, so close the connection.
		c.conn.CloseNow()
		return errors.New("client queue is full")
	}
}

// sendError sends an Error message that refers to the request seq.
func (c *client) sendError(seq int64, msg string) error {
	err := c.send("error", protocol.Error{
		Seq:     seq,
		Message: msg,
	})
	return err
}

// writeLoop sends each message from the queue, and gives each message the next Seq.
// writeLoop closes the connection when it returns.
func (c *client) writeLoop(ctx context.Context) error {
	defer c.conn.CloseNow()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-c.queue:
			c.seq++
			e.Seq = c.seq
			encoded, err := json.Marshal(e)
			if err != nil {
				return err
			}
			writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = c.conn.Write(writeCtx, websocket.MessageText, encoded)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

// Run handles the events from the connections and the watcher, one at a time.
// Run returns when ctx is done.
func (s *session) Run(ctx context.Context) error {
	defer close(s.done)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-s.events:
			switch ev.kind {
			case eventMessage:
				s.handleMessage(ev.client, ev.data)
			case eventDisconnect:
				_, registered := s.clients[ev.client]
				delete(s.clients, ev.client)
				if registered && !ev.client.editor {
					s.sendStatus()
				}
			case EventDiskChanged:
				s.handleDiskChange()
			}
		}
	}
}

// handleMessage decodes one message from c and sends it to its handler.
// Each message type except "hello" needs a completed Hello first.
func (s *session) handleMessage(c *client, data []byte) {
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		c.sendError(0, "message isn't valid JSON")
		return
	}

	_, registered := s.clients[c]

	switch env.Type {
	case "hello":
		if registered {
			c.sendError(env.Seq, "already said hello")
			return
		}
		s.handleHello(c, env)
	case "patch":
		if !registered {
			c.sendError(env.Seq, "patch sent before hello")
			return
		}
		s.handlePatch(c, env)
	case "locate":
		if !registered {
			c.sendError(env.Seq, "locate sent before hello")
			return
		}
		s.handleLocate(c, env)
	case "ack", "error":
		// The server does not use these messages yet.
	default:
		c.sendError(env.Seq, "unknown message type: "+env.Type)
	}
}

// handleHello checks the protocol version and the place ID, and sends a Snapshot to c.
// c becomes a registered client only after the Snapshot is in its queue.
func (s *session) handleHello(c *client, e protocol.Envelope) {
	var hello protocol.Hello
	if err := json.Unmarshal(e.Data, &hello); err != nil {
		c.sendError(e.Seq, "hello is not valid")
		return
	}

	if hello.ProtocolVersion != protocolVersion {
		c.sendError(e.Seq, fmt.Sprintf("protocol version mismatch, expected <%d>, got <%d>", protocolVersion, hello.ProtocolVersion))
		return
	}

	c.editor = hello.Role == protocol.RoleEditor
	places := s.cfg.Serve.PlaceIds
	if !c.editor && len(places) > 0 && !slices.Contains(places, hello.PlaceID) {
		c.sendError(e.Seq, fmt.Sprintf("place %d is not in the allowed place files for this project", hello.PlaceID))
		return
	}

	snapshot := protocol.Snapshot{
		Root:      s.t.Root(),
		OnConnect: string(s.cfg.Conflicts.OnConnect),
		Ignore: protocol.Ignore{
			Instances: s.cfg.Ignore.Instances,
			Classes:   s.cfg.Ignore.Classes,
		},
		Mounts:    s.mounts,
		Instances: s.t.All(),
	}
	if err := c.send("snapshot", snapshot); err != nil {
		s.logger.Warn("send snapshot", zap.Error(err))
		return
	}
	s.clients[c] = struct{}{}

	if !c.editor {
		s.sendStatus()
	} else if err := c.send("status", s.status()); err != nil {
		s.logger.Warn("send status", zap.Error(err))
	}
}

// status counts the connected Studio plugins.
func (s *session) status() protocol.Status {
	var status protocol.Status
	for c := range s.clients {
		if !c.editor {
			status.Plugins++
		}
	}
	return status
}

// sendStatus sends the Status to each editor. It does not send Status to plugins, because plugins do not know this message.
func (s *session) sendStatus() {
	status := s.status()
	for c := range s.clients {
		if !c.editor {
			continue
		}
		if err := c.send("status", status); err != nil {
			s.logger.Warn("send status", zap.Error(err))
		}
	}
}

// handleLocate replies to a Locate with a Located message.
func (s *session) handleLocate(c *client, e protocol.Envelope) {
	var locate protocol.Locate
	if err := json.Unmarshal(e.Data, &locate); err != nil {
		c.sendError(e.Seq, "locate is not valid")
		return
	}
	located, err := project.Locate(s.cfg, s.t, s.mounts, locate.ID)
	if err != nil {
		c.sendError(e.Seq, err.Error())
		return
	}
	located.Seq = e.Seq
	if err := c.send("located", located); err != nil {
		s.logger.Warn("send located", zap.Error(err))
	}
}

// handlePatch applies a patch from c to the tree and to the disk.
// It then sends the applied part of the patch to each other client.
// It sends nothing back to c, because c already has the changes.
// The file writes cause a rescan, but the rescan finds no difference, so the changes do not return to c.
func (s *session) handlePatch(c *client, e protocol.Envelope) {
	var patch protocol.Patch
	if err := json.Unmarshal(e.Data, &patch); err != nil {
		c.sendError(e.Seq, "patch is not valid")
		return
	}

	applied, errs := project.Apply(s.cfg, s.t, s.mounts, patch)
	for _, err := range errs {
		s.logger.Warn("apply plugin patch", zap.Error(err))
		c.sendError(e.Seq, err.Error())
	}
	if applied.Empty() {
		return
	}

	for other := range s.clients {
		if other == c {
			continue
		}
		if err := other.send("patch", applied); err != nil {
			s.logger.Warn("forward patch", zap.Error(err))
		}
	}
}

// handleDiskChange scans the disk again and sends the difference to each client.
// If the scan fails, the current tree stays the same.
func (s *session) handleDiskChange() {
	next, _, err := project.Load(s.cfg)
	if err != nil {
		s.logger.Warn("rescan failed, keeping current tree", zap.Error(err))
		return
	}

	patch := tree.Diff(s.t, next)
	if patch.Empty() {
		return
	}

	if err := s.t.Apply(patch); err != nil {
		s.logger.Error("apply disk patch", zap.Error(err))
		return
	}

	for c := range s.clients {
		if err := c.send("patch", patch); err != nil {
			s.logger.Warn("send patch", zap.Error(err))
		}
	}

	// The patch may have removed the file that made an instance fileless.
	// An empty Apply tries the writes for the fileless instances again.
	if _, errs := project.Apply(s.cfg, s.t, s.mounts, protocol.Patch{}); len(errs) > 0 {
		s.logger.Warn("retry writes", zap.Errors("errors", errs))
	}
}

// DiskChanged tells Run to scan the disk again. The watcher calls DiskChanged.
func (s *session) DiskChanged() {
	s.post(event{kind: EventDiskChanged})
}

// post gives ev to Run. post waits until Run takes ev or until Run returns.
func (s *session) post(ev event) {
	select {
	case s.events <- ev:
	case <-s.done:

	}
}

// Handle serves one WebSocket connection until the connection closes or ctx is done.
// Handle returns nil after a normal close from the client.
func (s *session) Handle(ctx context.Context, conn *websocket.Conn) error {
	conn.SetReadLimit(64 << 20)
	c := &client{
		conn:  conn,
		queue: make(chan protocol.Envelope, 256),
		seq:   0,
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		err := c.writeLoop(ctx)
		if err != nil {
			s.logger.Warn("error in write loop", zap.Error(err))
		}
	}()
	defer s.post(event{kind: eventDisconnect, client: c})
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
				return nil
			}
			return err
		}

		s.post(event{kind: eventMessage, client: c, data: data})
	}
}
