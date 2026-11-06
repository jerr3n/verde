package session

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jerr3n/verde/executable/internal/config"
	"github.com/jerr3n/verde/executable/internal/project"
	"github.com/jerr3n/verde/executable/internal/protocol"
	"github.com/jerr3n/verde/executable/internal/watch"
	"go.uber.org/zap"
)

// quietPeriod is longer than the watcher's debounce plus a rescan, so a message that
// hasn't arrived after this isn't coming.
const quietPeriod = 500 * time.Millisecond

// server runs the real session and watcher against a temporary project.
type server struct {
	t   *testing.T
	cfg *config.Cfg
	url string
}

func startServer(t *testing.T, files map[string]string) *server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, config.DefaultPath)
	yml := "serve: {port: 1234}\nsync:\n  - {instance: W, path: src}\n"
	if err := os.WriteFile(configPath, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	tr, mounts, err := project.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := New(cfg, tr, mounts, zap.NewNop())
	go s.Run(ctx)
	go watch.Run(ctx, cfg, s.DiskChanged, zap.NewNop())

	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		s.Handle(r.Context(), conn)
	}))
	t.Cleanup(httpServer.Close)
	return &server{t, cfg, "ws" + strings.TrimPrefix(httpServer.URL, "http")}
}

// plugin is a test client that speaks the protocol the way the Studio plugin does.
type plugin struct {
	t        *testing.T
	conn     *websocket.Conn
	seq      int64
	messages chan protocol.Envelope
	snapshot protocol.Snapshot
}

func (s *server) connect() *plugin {
	s.t.Helper()
	conn, _, err := websocket.Dial(context.Background(), s.url, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { conn.CloseNow() })

	p := &plugin{t: s.t, conn: conn, messages: make(chan protocol.Envelope, 64)}
	go func() {
		defer close(p.messages)
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var env protocol.Envelope
			if json.Unmarshal(data, &env) == nil {
				p.messages <- env
			}
		}
	}()

	p.send("hello", protocol.Hello{ProtocolVersion: protocolVersion, Client: "test"})
	env := p.next()
	if env.Type != "snapshot" {
		s.t.Fatalf("got %s, want snapshot: %s", env.Type, env.Data)
	}
	if err := json.Unmarshal(env.Data, &p.snapshot); err != nil {
		s.t.Fatal(err)
	}
	return p
}

func (p *plugin) send(msgType string, data any) {
	p.t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		p.t.Fatal(err)
	}
	p.seq++
	message, err := json.Marshal(protocol.Envelope{Type: msgType, Seq: p.seq, Data: encoded})
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.conn.Write(context.Background(), websocket.MessageText, message); err != nil {
		p.t.Fatal(err)
	}
}

func (p *plugin) next() protocol.Envelope {
	p.t.Helper()
	select {
	case env, ok := <-p.messages:
		if !ok {
			p.t.Fatal("connection closed")
		}
		return env
	case <-time.After(5 * time.Second):
		p.t.Fatal("timed out waiting for a message")
	}
	panic("unreachable")
}

func (p *plugin) nextPatch() protocol.Patch {
	p.t.Helper()
	env := p.next()
	if env.Type != "patch" {
		p.t.Fatalf("got %s, want patch: %s", env.Type, env.Data)
	}
	var patch protocol.Patch
	if err := json.Unmarshal(env.Data, &patch); err != nil {
		p.t.Fatal(err)
	}
	return patch
}

func (p *plugin) assertQuiet() {
	p.t.Helper()
	select {
	case env, ok := <-p.messages:
		if ok {
			p.t.Errorf("unexpected %s: %s", env.Type, env.Data)
		}
	case <-time.After(quietPeriod):
	}
}

// ref finds a snapshot instance by name.
func (p *plugin) ref(name string) protocol.Ref {
	p.t.Helper()
	for _, inst := range p.snapshot.Instances {
		if inst.Name == name {
			return inst.ID
		}
	}
	p.t.Fatalf("no %s in the snapshot", name)
	return ""
}

func source(text string) map[string]protocol.Value {
	encoded, _ := json.Marshal(text)
	return map[string]protocol.Value{"Source": {Type: "String", Value: encoded}}
}

func (s *server) waitForFile(name, want string) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	var err error
	for time.Now().Before(deadline) {
		got, err = os.ReadFile(filepath.Join(s.cfg.Root, name))
		if err == nil && string(got) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Errorf("%s = %q, %v, want %q", name, got, err, want)
}

func (s *server) waitForMissing(name string) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(s.cfg.Root, name)); os.IsNotExist(err) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.t.Errorf("%s still exists", name)
}

func TestPatchReachesDiskWithoutEcho(t *testing.T) {
	s := startServer(t, map[string]string{"src/Main.server.luau": "print('old')\n"})
	studio := s.connect()
	main, mount := studio.ref("Main"), studio.ref("W")

	studio.send("patch", protocol.Patch{Updated: []protocol.Update{{ID: main, Props: source("print('new')\n")}}})
	s.waitForFile("src/Main.server.luau", "print('new')\n")
	studio.assertQuiet()

	studio.send("patch", protocol.Patch{Updated: []protocol.Update{{ID: main, Name: "Entry"}}})
	s.waitForFile("src/Entry.server.luau", "print('new')\n")
	s.waitForMissing("src/Main.server.luau")
	studio.assertQuiet()

	studio.send("patch", protocol.Patch{Added: []protocol.Instance{
		{ID: "11111111-new", Parent: mount, Name: "lib", Class: "Folder"},
		{ID: "22222222-new", Parent: "11111111-new", Name: "Util", Class: "ModuleScript", Props: source("return {}\n")},
	}})
	s.waitForFile("src/lib/Util.luau", "return {}\n")
	studio.assertQuiet()

	studio.send("patch", protocol.Patch{Removed: []protocol.Ref{"22222222-new", "11111111-new"}})
	s.waitForMissing("src/lib")
	studio.assertQuiet()
}

func TestPatchForwardedToOtherPluginsOnly(t *testing.T) {
	s := startServer(t, map[string]string{"src/Main.server.luau": "old"})
	sender, other := s.connect(), s.connect()

	sender.send("patch", protocol.Patch{Updated: []protocol.Update{{ID: sender.ref("Main"), Props: source("new")}}})

	got := other.nextPatch()
	if len(got.Updated) != 1 || got.Updated[0].ID != sender.ref("Main") {
		t.Errorf("forwarded %+v", got)
	}
	sender.assertQuiet()
	other.assertQuiet()
}

func TestDiskEditStillReachesPluginsAfterPatch(t *testing.T) {
	s := startServer(t, map[string]string{"src/Main.server.luau": "old"})
	studio := s.connect()
	main := studio.ref("Main")

	studio.send("patch", protocol.Patch{Updated: []protocol.Update{{ID: main, Props: source("from studio")}}})
	s.waitForFile("src/Main.server.luau", "from studio")
	studio.assertQuiet()

	if err := os.WriteFile(filepath.Join(s.cfg.Root, "src/Main.server.luau"), []byte("from disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := studio.nextPatch()
	if len(got.Updated) != 1 || got.Updated[0].ID != main {
		t.Fatalf("patch = %+v", got)
	}
	var text string
	json.Unmarshal(got.Updated[0].Props["Source"].Value, &text)
	if text != "from disk" {
		t.Errorf("Source = %q", text)
	}
}

func TestFolderMadeByStudioIsWatched(t *testing.T) {
	s := startServer(t, nil) // no src folder at all
	studio := s.connect()

	studio.send("patch", protocol.Patch{Added: []protocol.Instance{
		{ID: "aaaaaaaa-new", Parent: studio.ref("W"), Name: "lib", Class: "Folder"},
		{ID: "bbbbbbbb-new", Parent: "aaaaaaaa-new", Name: "Util", Class: "ModuleScript", Props: source("a")},
	}})
	s.waitForFile("src/lib/Util.luau", "a")
	studio.assertQuiet()

	if err := os.WriteFile(filepath.Join(s.cfg.Root, "src/lib/Util.luau"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := studio.nextPatch(); len(got.Updated) != 1 || got.Updated[0].ID != "bbbbbbbb-new" {
		t.Errorf("patch = %+v", got)
	}
}

func TestReconnectSeesWhatStudioWrote(t *testing.T) {
	s := startServer(t, map[string]string{"src/.keep": ""})
	first := s.connect()
	first.send("patch", protocol.Patch{Added: []protocol.Instance{
		{ID: "cccccccc-new", Parent: first.ref("W"), Name: "Lamp", Class: "Part"},
		{ID: "dddddddd-new", Parent: first.ref("W"), Name: "Main", Class: "Script", Props: source("x")},
	}})
	s.waitForFile("src/Main.server.luau", "x")
	first.assertQuiet()

	second := s.connect()
	if got := second.ref("Lamp"); got != "cccccccc-new" {
		t.Errorf("Lamp = %s", got)
	}
	if got := second.ref("Main"); got != "dddddddd-new" {
		t.Errorf("Main = %s", got)
	}
}

func TestPartFromStudioReachesDiskWithoutEcho(t *testing.T) {
	s := startServer(t, map[string]string{"src/.keep": ""})
	p := s.connect()
	p.send("patch", protocol.Patch{Added: []protocol.Instance{{
		ID: "cccccccc-new", Parent: p.ref("W"), Name: "Lamp", Class: "Part",
		Props: map[string]protocol.Value{"Size": {Type: "Vector3", Value: []byte(`[4,1,2]`)}},
	}}})
	s.waitForFile("src/Lamp.Part.inst.json", "{\n  \"props\": {\n    \"Size\": {\"t\":\"Vector3\",\"v\":[4,1,2]}\n  }\n}\n")
	p.assertQuiet()

	p.send("patch", protocol.Patch{Updated: []protocol.Update{{
		ID:    "cccccccc-new",
		Props: map[string]protocol.Value{"Size": {Type: "Vector3", Value: []byte(`[8,1,2]`)}},
	}}})
	s.waitForFile("src/Lamp.Part.inst.json", "{\n  \"props\": {\n    \"Size\": {\"t\":\"Vector3\",\"v\":[8,1,2]}\n  }\n}\n")
	p.assertQuiet()
}

func TestEditingPartFileReachesPlugin(t *testing.T) {
	s := startServer(t, map[string]string{
		"src/Lamp.Part.inst.json": `{"props": {"Anchored": {"t": "Bool", "v": true}}}`,
	})
	p := s.connect()
	lamp := p.ref("Lamp")
	full := filepath.Join(s.cfg.Root, "src/Lamp.Part.inst.json")

	// only a change in meaning should reach studio
	if err := os.WriteFile(full, []byte(`{"props":{"Anchored":{"v":true,"t":"Bool"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p.assertQuiet()

	if err := os.WriteFile(full, []byte(`{"props":{"Anchored":{"t":"Bool","v":false}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := p.nextPatch()
	if len(got.Updated) != 1 || got.Updated[0].ID != lamp || string(got.Updated[0].Props["Anchored"].Value) != "false" {
		t.Errorf("patch = %+v", got)
	}

	// a half-saved file keeps the current tree, instead of deleting the part from studio
	if err := os.WriteFile(full, []byte(`{"props":`), 0o644); err != nil {
		t.Fatal(err)
	}
	p.assertQuiet()

	if err := os.Remove(full); err != nil {
		t.Fatal(err)
	}
	if got := p.nextPatch(); len(got.Removed) != 1 || got.Removed[0] != lamp {
		t.Errorf("patch = %+v", got)
	}
}

func TestFailedChangeSendsErrorAndKeepsConnection(t *testing.T) {
	s := startServer(t, map[string]string{"src/Main.luau": "x"})
	studio := s.connect()

	studio.send("patch", protocol.Patch{Removed: []protocol.Ref{studio.ref("W")}})
	env := studio.next()
	if env.Type != "error" || env.Seq == 0 {
		t.Fatalf("got %s: %s", env.Type, env.Data)
	}
	var failure protocol.Error
	if err := json.Unmarshal(env.Data, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Seq != studio.seq {
		t.Errorf("error is about message %d, want %d", failure.Seq, studio.seq)
	}
	s.waitForFile("src/Main.luau", "x")

	studio.send("patch", protocol.Patch{Updated: []protocol.Update{{ID: studio.ref("Main"), Props: source("y")}}})
	s.waitForFile("src/Main.luau", "y")
}

func TestPatchBeforeHelloIsRejected(t *testing.T) {
	s := startServer(t, map[string]string{"src/Main.luau": "x"})
	conn, _, err := websocket.Dial(context.Background(), s.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	message := `{"type":"patch","seq":1,"data":{"removed":["anything"]}}`
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(message)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != "error" {
		t.Errorf("got %s, want error", env.Type)
	}
}
