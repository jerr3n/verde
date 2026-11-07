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

const protocolVersion = 1

// client is one connected plugin.
type client struct {
	conn  *websocket.Conn
	queue chan protocol.Envelope // messages waiting for the writer goroutine
	seq   int64                  // last seq sent. Only the writer goroutine touches this

	editor bool // said hello as protocol.RoleEditor. Only Run touches this
}

type eventKind int

const (
	eventMessage eventKind = iota
	eventDisconnect
	EventDiskChanged
)

// event is something a connection asks Run to handle.
type event struct {
	kind   eventKind
	client *client
	data   []byte // only set for eventMessage
}

type session struct {
	cfg    *config.Cfg
	t      *tree.Tree
	mounts []protocol.Mount
	logger *zap.Logger

	clients map[*client]struct{} // clients that have completed hello. Only Run touches this
	events  chan event           // connections send their events to Run here
	done    chan struct{}        // closed when Run exits
}

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
		c.conn.CloseNow() // best option
		return errors.New("client queue is full")
	}
}

func (c *client) sendError(seq int64, msg string) error {
	err := c.send("error", protocol.Error{
		Seq:     seq,
		Message: msg,
	})
	return err
}

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
		// Nothing to do for now.
	default:
		c.sendError(env.Seq, "unknown message type: "+env.Type)
	}
}

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

func (s *session) status() protocol.Status {
	var status protocol.Status
	for c := range s.clients {
		if !c.editor {
			status.Plugins++
		}
	}
	return status
}

// sendStatus tells every editor what's connected. Plugins don't know this message.
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

// handlePatch applies a patch from a plugin to the tree and disk, then forwards what
// was applied to the other plugins. The sender already has these changes, and the
// rescan its file writes cause finds nothing different, so nothing echoes back.
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

	// A file this removed may have been what kept an instance fileless.
	if _, errs := project.Apply(s.cfg, s.t, s.mounts, protocol.Patch{}); len(errs) > 0 {
		s.logger.Warn("retry writes", zap.Errors("errors", errs))
	}
}

func (s *session) DiskChanged() {
	s.post(event{kind: EventDiskChanged})
}

// post hands an event to run, waiting until Run takes it or has exited.
func (s *session) post(ev event) {
	select {
	case s.events <- ev:
	case <-s.done:

	}
}

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
