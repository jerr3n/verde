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
				delete(s.clients, ev.client)
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
		s.logger.Info("received patch, not applied yet") // step 8 goes here
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

	places := s.cfg.Serve.PlaceIds
	if len(places) > 0 && !slices.Contains(places, hello.PlaceID) {
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
}

func (s *session) handleDiskChange() {
	next, _, err := project.Load(s.cfg)
	if err != nil {
		s.logger.Warn("rescan failed, keeping current tree", zap.Error(err))
		return
	}

	patch := tree.Diff(s.t, next)
	if len(patch.Added)+len(patch.Updated)+len(patch.Removed) == 0 {
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
