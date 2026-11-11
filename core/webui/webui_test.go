package webui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newTestClient(h *Hub) *Client {
	c := &Client{send: make(chan Event, 32), hub: h, done: make(chan struct{})}
	h.add(c)
	return c
}

func nextEvent(t *testing.T, c *Client) Event {
	t.Helper()
	select {
	case ev := <-c.send:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func TestHubLifecycle(t *testing.T) {
	h := NewHub()
	if h.Count() != 0 {
		t.Fatalf("count = %d", h.Count())
	}

	var gotHandler bool
	h.SetMessageHandler(func(*Client, []byte) { gotHandler = true })

	auth := newEvent("telegram_qr", map[string]any{"qr": "x"})
	h.setAuthEvent(auth)

	c := newTestClient(h)
	if h.Count() != 1 {
		t.Fatalf("count = %d", h.Count())
	}
	// add() should deliver saved auth event to new client
	if ev := nextEvent(t, c); ev.Type != "telegram_qr" {
		t.Fatalf("auth event = %q", ev.Type)
	}

	h.Broadcast(newEvent("broadcast", 1))
	if ev := nextEvent(t, c); ev.Type != "broadcast" {
		t.Fatalf("broadcast = %q", ev.Type)
	}

	h.SendTo(c, newEvent("direct", 2))
	if ev := nextEvent(t, c); ev.Type != "direct" {
		t.Fatalf("direct = %q", ev.Type)
	}

	h.BroadcastTo(c, newEvent("broadcast-to", 3))
	if ev := nextEvent(t, c); ev.Type != "broadcast-to" {
		t.Fatalf("broadcastTo = %q", ev.Type)
	}

	// closed client does not block broadcast
	c.stopSending()
	c.stopSending() // idempotent
	h.Broadcast(newEvent("after-done", 4))

	h.clearAuthEvent()
	h.remove(c)
	if h.Count() != 0 {
		t.Fatalf("count after remove = %d", h.Count())
	}

	h.mu.RLock()
	handler := h.onMessage
	h.mu.RUnlock()
	handler(c, nil)
	if !gotHandler {
		t.Fatal("message handler not set")
	}
}

func TestPrompterNoClients(t *testing.T) {
	p := NewPrompter(NewHub())
	if _, err := p.Text("q", "d"); err == nil {
		t.Fatal("Text without clients must fail")
	}
	if v, err := p.YesNo("q", true); err == nil || !v {
		t.Fatalf("YesNo = %v, %v", v, err)
	}
	if v, err := p.Select("q", []string{"a"}, 0); err == nil || v != 0 {
		t.Fatalf("Select = %v, %v", v, err)
	}
}

func TestPrompterTextAndRunHelpers(t *testing.T) {
	h := NewHub()
	p := NewPrompter(h)
	c := newTestClient(h)

	done := make(chan struct{})
	var answer string
	var err error
	go func() {
		answer, err = p.Text("name?", "default")
		close(done)
	}()

	ev := nextEvent(t, c)
	if ev.Type != EvtPrompt {
		t.Fatalf("event = %q", ev.Type)
	}
	payload := ev.Data.(PromptPayload)
	if payload.Kind != "text" || payload.Question != "name?" {
		t.Fatalf("payload = %+v", payload)
	}
	reply, _ := json.Marshal(map[string]any{
		"type": EvtPromptReply,
		"data": map[string]any{"id": payload.ID, "value": "Alice"},
	})
	p.onMessage(c, reply)
	<-done
	if err != nil || answer != "Alice" {
		t.Fatalf("Text = %q, %v", answer, err)
	}

	// empty answer falls back to default
	done = make(chan struct{})
	go func() { answer, err = p.Text("q", "fallback"); close(done) }()
	ev = nextEvent(t, c)
	payload = ev.Data.(PromptPayload)
	reply, _ = json.Marshal(map[string]any{"type": EvtPromptReply, "data": map[string]any{"id": payload.ID, "value": ""}})
	p.onMessage(c, reply)
	<-done
	if err != nil || answer != "fallback" {
		t.Fatalf("default answer = %q, %v", answer, err)
	}
}

func TestPrompterYesNoAndSelect(t *testing.T) {
	h := NewHub()
	p := NewPrompter(h)
	c := newTestClient(h)

	// YesNo: bool.
	done := make(chan bool)
	var yes bool
	go func() { yes, _ = p.YesNo("ok?", false); done <- true }()
	ev := nextEvent(t, c)
	id := ev.Data.(PromptPayload).ID
	reply, _ := json.Marshal(map[string]any{"type": EvtPromptReply, "data": map[string]any{"id": id, "value": true}})
	p.onMessage(c, reply)
	<-done
	if !yes {
		t.Fatal("YesNo = false")
	}

	// YesNo: non-bool -> default
	go func() { yes, _ = p.YesNo("ok?", true); done <- true }()
	ev = nextEvent(t, c)
	id = ev.Data.(PromptPayload).ID
	reply, _ = json.Marshal(map[string]any{"type": EvtPromptReply, "data": map[string]any{"id": id, "value": "yes"}})
	p.onMessage(c, reply)
	<-done
	if !yes {
		t.Fatal("YesNo must fall back to default")
	}

	// Select: valid index
	sel := make(chan int)
	var idx int
	go func() { idx, _ = p.Select("pick", []string{"a", "b"}, 0); sel <- idx }()
	ev = nextEvent(t, c)
	id = ev.Data.(PromptPayload).ID
	reply, _ = json.Marshal(map[string]any{"type": EvtPromptReply, "data": map[string]any{"id": id, "value": 1}})
	p.onMessage(c, reply)
	if got := <-sel; got != 1 {
		t.Fatalf("Select = %d", got)
	}

	// Select: out of range index -> default
	go func() { idx, _ = p.Select("pick", []string{"a"}, 5); sel <- idx }()
	ev = nextEvent(t, c)
	id = ev.Data.(PromptPayload).ID
	reply, _ = json.Marshal(map[string]any{"type": EvtPromptReply, "data": map[string]any{"id": id, "value": 99}})
	p.onMessage(c, reply)
	if got := <-sel; got != 5 {
		t.Fatalf("Select fallback = %d", got)
	}
}

func TestPrompterOnMessageValidation(t *testing.T) {
	h := NewHub()
	p := NewPrompter(h)
	c := newTestClient(h)

	// invalid json and unknown types are ignored
	p.onMessage(c, []byte("not json"))
	p.onMessage(c, []byte(`{"type":"unknown"}`))
	select {
	case ev := <-c.send:
		t.Fatalf("unexpected event %q", ev.Type)
	case <-time.After(20 * time.Millisecond):
	}

	// malformed prompt reply payload
	p.onMessage(c, []byte(`{"type":"prompt_reply","data":"oops"}`))
	p.onMessage(c, []byte(`{"type":"prompt_reply","data":{"id":"missing","value":1}}`))

	// widget click without handler
	p.onMessage(c, []byte(`{"type":"widget_click","data":{"id":"w","payload":"p"}}`))
	p.onMessage(c, []byte(`{"type":"widget_click","data":"bad"}`))
	select {
	case ev := <-c.send:
		t.Fatalf("unexpected widget event %q", ev.Type)
	case <-time.After(20 * time.Millisecond):
	}

	// handler with result -> widget_data
	p.SetWidgetClickHandler(func(id, payload string) string {
		if id != "w" || payload != "p" {
			return ""
		}
		return `{"ok":true}`
	})
	p.onMessage(c, []byte(`{"type":"widget_click","data":{"id":"w","payload":"p"}}`))
	ev := nextEvent(t, c)
	if ev.Type != EvtWidgetData {
		t.Fatalf("event = %q", ev.Type)
	}
	if data := ev.Data.(WidgetDataPayload); data.ID != "w" || data.Data != `{"ok":true}` {
		t.Fatalf("data = %+v", data)
	}

	// handler without result sends nothing
	p.SetWidgetClickHandler(func(string, string) string { return "" })
	p.onMessage(c, []byte(`{"type":"widget_click","data":{"id":"w","payload":"p"}}`))
	select {
	case ev := <-c.send:
		t.Fatalf("unexpected event %q", ev.Type)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestWaitForReplyTimeout(t *testing.T) {
	p := NewPrompter(NewHub())
	if _, err := p.waitForReply("id", 10*time.Millisecond); err == nil {
		t.Fatal("expected timeout")
	}
	p.mu.Lock()
	_, ok := p.replies["id"]
	p.mu.Unlock()
	if ok {
		t.Fatal("reply channel leaked after timeout")
	}
}

func TestNewID(t *testing.T) {
	a, b := newID(), newID()
	if len(a) != 16 || len(b) != 16 || a == b {
		t.Fatalf("newID = %q, %q", a, b)
	}
}

func TestInstanceSenders(t *testing.T) {
	h := NewHub()
	c := newTestClient(h)
	inst := &Instance{hub: h, logFn: func(string, ...any) {}}

	inst.Log("INFO", "core", "hello")
	if ev := nextEvent(t, c); ev.Type != EvtLog {
		t.Fatalf("log = %q", ev.Type)
	}
	inst.Metrics(1, 2, 3)
	if ev := nextEvent(t, c); ev.Type != EvtMetrics {
		t.Fatalf("metrics = %q", ev.Type)
	}
	inst.Stats(4, 5, 6)
	if ev := nextEvent(t, c); ev.Type != EvtStats {
		t.Fatalf("stats = %q", ev.Type)
	}
	inst.Transport("tg", "telegram", "up")
	if ev := nextEvent(t, c); ev.Type != EvtTransport {
		t.Fatalf("transport = %q", ev.Type)
	}
	inst.PluginList([]PluginPayload{{Name: "p"}})
	if ev := nextEvent(t, c); ev.Type != EvtPlugin {
		t.Fatalf("plugins = %q", ev.Type)
	}

	id := inst.StreamStart(42, "Alice")
	if id == "" {
		t.Fatal("empty stream id")
	}
	if ev := nextEvent(t, c); ev.Type != EvtStreamStart {
		t.Fatalf("stream start = %q", ev.Type)
	}
	inst.StreamToken(id, "delta")
	if ev := nextEvent(t, c); ev.Type != EvtStreamToken {
		t.Fatalf("stream token = %q", ev.Type)
	}
	inst.StreamEnd(id, "answer", errors.New("boom"))
	ev := nextEvent(t, c)
	if payload := ev.Data.(StreamEndPayload); payload.Error != "boom" || payload.Answer != "answer" {
		t.Fatalf("stream end = %+v", payload)
	}

	inst.WidgetData("w", `{"n":1}`)
	if ev := nextEvent(t, c); ev.Type != EvtWidgetData {
		t.Fatalf("widget data = %q", ev.Type)
	}
	inst.WidgetNotify("w", 2, "warn", 500)
	if ev := nextEvent(t, c); ev.Type != EvtWidgetNotify {
		t.Fatalf("widget notify = %q", ev.Type)
	}

	// empty arguments and nil receiver do not panic
	inst.StreamToken("", "")
	inst.StreamEnd("", "", nil)
	inst.WidgetData("", "")
	var nilInst *Instance
	nilInst.Log("", "", "")
	nilInst.Metrics(0, 0, 0)
	nilInst.Stats(0, 0, 0)
	nilInst.Transport("", "", "")
	nilInst.PluginList(nil)
	if got := nilInst.StreamStart(0, ""); got != "" {
		t.Fatalf("nil StreamStart = %q", got)
	}
	nilInst.WidgetNotify("", 0, "", 0)

	select {
	case ev := <-c.send:
		t.Fatalf("unexpected event %q", ev.Type)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestInstanceAuthEvent(t *testing.T) {
	h := NewHub()
	c := newTestClient(h)
	inst := &Instance{hub: h}

	inst.AuthEvent(`{"type":"telegram_qr","data":{"qr":"abc"}}`)
	ev := nextEvent(t, c)
	if ev.Type != "telegram_qr" {
		t.Fatalf("event = %q", ev.Type)
	}
	h.mu.RLock()
	stored := h.authEvent
	h.mu.RUnlock()
	if stored == nil || stored.Type != "telegram_qr" {
		t.Fatal("auth event not stored")
	}

	inst.AuthEvent(`{"type":"telegram_auth_state","data":{"state":"waiting_qr"}}`)
	if ev := nextEvent(t, c); ev.Type != "telegram_auth_state" {
		t.Fatalf("event = %q", ev.Type)
	}
	h.mu.RLock()
	stored = h.authEvent
	h.mu.RUnlock()
	if stored == nil {
		t.Fatal("waiting_qr must not clear the QR event")
	}

	inst.AuthEvent(`{"type":"telegram_auth_state","data":{"state":"connected"}}`)
	if ev := nextEvent(t, c); ev.Type != "telegram_auth_state" {
		t.Fatalf("event = %q", ev.Type)
	}
	h.mu.RLock()
	stored = h.authEvent
	h.mu.RUnlock()
	if stored != nil {
		t.Fatal("auth event not cleared")
	}

	// invalid/foreign events are ignored
	inst.AuthEvent("not json")
	inst.AuthEvent(`{"type":"other","data":{}}`)
	inst.AuthEvent(`{"type":"telegram_qr","data":[}`)
	var nilInst *Instance
	nilInst.AuthEvent("{}")
	select {
	case ev := <-c.send:
		t.Fatalf("unexpected event %q", ev.Type)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestInstanceAccessors(t *testing.T) {
	var nilInst *Instance
	if nilInst.Hub() != nil || nilInst.Prompter() != nil || nilInst.URL() != "" {
		t.Fatal("nil instance accessors must be empty")
	}
	if cfg := nilInst.Config(); cfg.Enabled {
		t.Fatal("nil Config")
	}
	nilInst.Stop() // no panic

	inst := &Instance{cfg: Config{Port: 1}, hub: NewHub(), prompter: NewPrompter(NewHub())}
	inst.Stop() // server == nil
	if inst.Hub() == nil || inst.Prompter() == nil || inst.Config().Port != 1 {
		t.Fatal("accessors lost state")
	}
}

func TestSnapshotProvider(t *testing.T) {
	if got := (&snapshotProvider{}).Snapshot(); got.Version != "" {
		t.Fatalf("empty snapshot = %+v", got)
	}
	p := &snapshotProvider{fn: func() Snapshot { return Snapshot{Version: "v1", Tools: []string{"a"}} }}
	if got := p.Snapshot(); got.Version != "v1" || len(got.Tools) != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestStartAndStop(t *testing.T) {
	prev := Global()
	defer func() {
		mu.Lock()
		instance = prev
		mu.Unlock()
	}()

	if inst, err := Start(Config{Enabled: false}, nil, nil); err != nil || inst != nil {
		t.Fatalf("disabled Start = %v, %v", inst, err)
	}

	var logs []string
	inst, err := Start(Config{Enabled: true, Bind: "127.0.0.1", Port: 0}, func() Snapshot {
		return Snapshot{Version: "test", Tools: []string{}}
	}, func(format string, args ...any) {
		logs = append(logs, format)
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst == nil {
		t.Fatal("nil instance")
	}
	if Global() != inst {
		t.Fatal("Global() != started instance")
	}
	if inst.URL() == "" || !strings.HasPrefix(inst.URL(), "http://") {
		t.Fatalf("URL = %q", inst.URL())
	}
	if inst.Config().AuthToken == "" {
		t.Fatal("loopback server must generate auth token")
	}
	if len(logs) == 0 {
		t.Fatal("start logs missing")
	}

	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	resp, err := client.Get(inst.URL() + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d, %d bytes", resp.StatusCode, len(body))
	}

	inst.Stop()
	inst.Stop() // repeated stop is safe
}

func TestIsPortInUseError(t *testing.T) {
	if IsPortInUseError(nil) {
		t.Errorf("nil error should not be port in use")
	}
	if !IsPortInUseError(errors.New("listen tcp :8080: bind: address already in use")) {
		t.Errorf("expected true for address already in use")
	}
	if !IsPortInUseError(errors.New("bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.")) {
		t.Errorf("expected true for Windows socket error")
	}
	if !IsPortInUseError(errors.New("failed with WSAEADDRINUSE")) {
		t.Errorf("expected true for WSAEADDRINUSE")
	}
	if IsPortInUseError(errors.New("connection refused")) {
		t.Errorf("connection refused should not be port in use")
	}
}
