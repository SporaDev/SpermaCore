// package webui provides browser dashboard, websockets, and interactive ui.
package webui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// Config
type Config struct {
	Enabled   bool
	Bind      string
	Port      int
	AuthToken string
}

var Defaults = map[string]any{
	"enabled":    false,
	"bind":       "127.0.0.1",
	"port":       8080,
	"auth_token": "", // generated on first start
}

// defaults with generated auth_token
func DefaultsWithAuthToken() map[string]any {
	m := make(map[string]any, len(Defaults))
	for k, v := range Defaults {
		m[k] = v
	}
	if tok := generateToken(); tok != "" {
		m["auth_token"] = tok
	}
	return m
}

func Load(section map[string]any) Config {
	c := Config{
		Enabled: false,
		Bind:    "127.0.0.1",
		Port:    8080,
	}
	if section == nil {
		return c
	}
	if v, ok := getBool(section, "enabled"); ok {
		c.Enabled = v
	}
	if v, ok := getString(section, "bind"); ok && v != "" {
		c.Bind = v
	}
	if v, ok := getInt(section, "port"); ok && v > 0 && v < 65536 {
		c.Port = int(v)
	}
	if v, ok := getString(section, "auth_token"); ok {
		c.AuthToken = strings.TrimSpace(v)
	}
	return c
}

// Events
type Event struct {
	Type string `json:"type"`
	TS   int64  `json:"ts"`
	Data any    `json:"data"`
}

const (
	EvtHello       = "hello"
	EvtLog         = "log"
	EvtMetrics     = "metrics"
	EvtStats       = "stats"
	EvtTransport   = "transport"
	EvtPlugin      = "plugin"
	EvtPrompt      = "prompt"
	EvtPromptReply = "prompt_reply"

	// streaming
	EvtStreamStart = "stream_start"
	EvtStreamToken = "stream_token"
	EvtStreamEnd   = "stream_end"

	// widget events
	EvtWidgetData   = "widget_data"
	EvtWidgetNotify = "widget_notify"
)

func newEvent(typ string, data any) Event {
	return Event{
		Type: typ,
		TS:   time.Now().UnixMilli(),
		Data: data,
	}
}

// ---- Payloads ----

type LogPayload struct {
	Level string `json:"level"`
	Name  string `json:"name"`
	Msg   string `json:"msg"`
}

type MetricsPayload struct {
	QueueLen int64 `json:"queue_len"`
	Active   int64 `json:"active"`
	Errors   int64 `json:"errors"`
}

type StatsPayload struct {
	Messages  int64 `json:"messages"`
	ToolCalls int64 `json:"tool_calls"`
	Replies   int64 `json:"replies"`
}

type TransportPayload struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type PluginPayload struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Desc    string `json:"desc"`
	Enabled bool   `json:"enabled"`
	Loaded  bool   `json:"loaded"`
}

type PromptPayload struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"` // "text" | "yesno" | "select"
	Question string   `json:"question"`
	Default  any      `json:"default"`
	Options  []string `json:"options,omitempty"`
}

type PromptReplyPayload struct {
	ID    string `json:"id"`
	Value any    `json:"value"`
}

type StreamStartPayload struct {
	ID         string `json:"id"`
	ChatID     int64  `json:"chat_id"`
	SenderName string `json:"sender_name,omitempty"`
	TS         int64  `json:"ts"`
}

type StreamTokenPayload struct {
	ID    string `json:"id"`
	Delta string `json:"delta"`
}

type StreamEndPayload struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
	Error  string `json:"error,omitempty"`
}

// widget definition
type WidgetPayload struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Icon  string `json:"icon,omitempty"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	HTML  string `json:"html,omitempty"`
	Data  string `json:"data,omitempty"`
}

// widget data update payload
type WidgetDataPayload struct {
	ID   string `json:"id"`
	Data string `json:"data"`
}

// toast notification payload
type WidgetNotifyPayload struct {
	ID         string `json:"id"`
	Level      int    `json:"level"` // 0=info, 1=warn, 2=error, 3=success
	Msg        string `json:"msg"`
	DurationMs uint32 `json:"duration_ms,omitempty"`
}

// snapshot state for hello handshake
type Snapshot struct {
	Version    string             `json:"version"`
	OS         string             `json:"os"`
	PID        int                `json:"pid"`
	DataDir    string             `json:"data_dir"`
	Provider   string             `json:"provider"`
	Model      string             `json:"model"`
	Endpoint   string             `json:"endpoint"`
	Transports []TransportPayload `json:"transports"`
	Plugins    []PluginPayload    `json:"plugins"`
	Tools      []string           `json:"tools"`
	Widgets    []WidgetPayload    `json:"widgets"`
	Stats      StatsPayload       `json:"stats"`
	Metrics    MetricsPayload     `json:"metrics"`
	Uptime     int64              `json:"uptime_sec"`
}

type Client struct {
	conn      *websocket.Conn
	send      chan Event
	hub       *Hub
	done      chan struct{}
	closeOnce sync.Once
}

// close done channel to terminate writer goroutine
func (c *Client) stopSending() {
	c.closeOnce.Do(func() { close(c.done) })
}

type Hub struct {
	mu        sync.RWMutex
	clients   map[*Client]struct{}
	onMessage func(c *Client, raw []byte)
	authEvent *Event
}

func NewHub() *Hub {
	return &Hub{clients: make(map[*Client]struct{})}
}

func (h *Hub) SetMessageHandler(fn func(c *Client, raw []byte)) {
	h.mu.Lock()
	h.onMessage = fn
	h.mu.Unlock()
}

func (h *Hub) Broadcast(evt Event) {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- evt:
		case <-c.done:
		default:
		}
	}
}

func (h *Hub) SendTo(c *Client, evt Event) {
	select {
	case c.send <- evt:
	case <-c.done:
	default:
	}
}

func (h *Hub) BroadcastTo(c *Client, evt Event) {
	select {
	case c.send <- evt:
	case <-c.done:
	default:
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) add(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	authEvent := h.authEvent
	h.mu.Unlock()
	if authEvent != nil {
		h.SendTo(c, *authEvent)
	}
}

func (h *Hub) setAuthEvent(evt Event) {
	h.mu.Lock()
	copy := evt
	h.authEvent = &copy
	h.mu.Unlock()
}

func (h *Hub) clearAuthEvent() {
	h.mu.Lock()
	h.authEvent = nil
	h.mu.Unlock()
}

func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (c *Client) serve(ctx context.Context) {
	defer func() {
		c.hub.remove(c)
		c.stopSending()
		_ = c.conn.Close(websocket.StatusNormalClosure, "")
	}()

	go func() {
		pingTicker := time.NewTicker(15 * time.Second)
		defer pingTicker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-pingTicker.C:
				pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err := c.conn.Ping(pctx)
				cancel()
				if err != nil {
					_ = c.conn.Close(websocket.StatusGoingAway, "ping timeout")
					return
				}
			case evt := <-c.send:
				data, err := json.Marshal(evt)
				if err != nil {
					continue
				}
				wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = c.conn.Write(wctx, websocket.MessageText, data)
				cancel()
				if err != nil {
					_ = c.conn.Close(websocket.StatusInternalError, "write failed")
					return
				}
			}
		}
	}()

	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		c.hub.mu.RLock()
		handler := c.hub.onMessage
		c.hub.mu.RUnlock()
		if handler != nil {
			handler(c, data)
		}
	}
}

// Server
//
//go:embed static/*
var staticFS embed.FS

type Server struct {
	cfg   Config
	hub   *Hub
	state StateProvider
	auth  *authState

	srv *http.Server
	ln  net.Listener
}

type StateProvider interface {
	Snapshot() Snapshot
}

func NewServer(cfg Config, hub *Hub, state StateProvider) *Server {
	// generate temporary token for loopback bind if missing
	token := strings.TrimSpace(cfg.AuthToken)
	if token == "" && isLoopbackBind(cfg.Bind) {
		token = generateToken()
		cfg.AuthToken = token
	}
	return &Server{cfg: cfg, hub: hub, state: state, auth: newAuthState(token)}
}

// IsPortInUseError returns true if the error indicates a socket address collision (bind: address already in use)
func IsPortInUseError(err error) bool {
	if err == nil {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if syscallErr, ok := opErr.Err.(*os.SyscallError); ok {
			if errors.Is(syscallErr.Err, syscall.EADDRINUSE) {
				return true
			}
		}
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address") ||
		strings.Contains(msg, "wsaeaddrinuse") ||
		strings.Contains(msg, "eaddrinuse")
}

func (s *Server) Start() error {
	if !isLoopbackBind(s.cfg.Bind) && strings.TrimSpace(s.cfg.AuthToken) == "" {
		return fmt.Errorf("webui: refusing to listen on non-loopback %q without auth_token", s.cfg.Bind)
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Bind, s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("webui: listen %s: %w", addr, err)
	}
	s.ln = ln

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() { _ = s.srv.Serve(ln) }()
	return nil
}

func (s *Server) Stop() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	sub, err := fs.Sub(staticFS, "static")
	if err == nil {
		files := http.FileServer(http.FS(sub))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" || r.URL.Path == "/index.html" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

				// If token is provided in URL query, validate and set session cookie
				if tok := r.URL.Query().Get("token"); tok != "" && s.auth.tokenMatches(tok) {
					if sid, ok := s.auth.newSession(); ok {
						http.SetCookie(w, &http.Cookie{
							Name:     sessionCookie,
							Value:    sid,
							Path:     "/",
							HttpOnly: true,
							SameSite: http.SameSiteStrictMode,
							MaxAge:   int(s.auth.ttl / time.Second),
						})
					}
				} else if isLoopbackAddr(r.RemoteAddr) || s.checkOrigin(r) {
					// Auto-issue session cookie for loopback access if missing or invalid
					if c, err := r.Cookie(sessionCookie); err != nil || !s.auth.validSession(c.Value) {
						if sid, ok := s.auth.newSession(); ok {
							http.SetCookie(w, &http.Cookie{
								Name:     sessionCookie,
								Value:    sid,
								Path:     "/",
								HttpOnly: true,
								SameSite: http.SameSiteStrictMode,
								MaxAge:   int(s.auth.ttl / time.Second),
							})
						}
					}
				}
			}
			files.ServeHTTP(w, r)
		})
	}

	// login endpoint validates token directly
	mux.HandleFunc("/api/login", s.originGuard(s.handleLogin))

	mux.HandleFunc("/api/state", s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.state.Snapshot())
	}))

	mux.HandleFunc("/ws", s.requireAuth(s.handleWS))
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.checkOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "origin not allowed")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// origin already validated
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(64 * 1024)

	ctx := r.Context()
	c := &Client{
		conn: conn,
		send: make(chan Event, 512),
		hub:  s.hub,
		done: make(chan struct{}),
	}
	s.hub.add(c)

	s.hub.SendTo(c, newEvent(EvtHello, s.state.Snapshot()))
	c.serve(ctx)
}

// widget click callback handler
type WidgetClickHandler func(id, payloadJSON string) string

// qr login handler for external transport
type QRLoginHandler func() error

type Prompter struct {
	hub     *Hub
	replies map[string]chan any
	mu      sync.Mutex

	// widget click handler
	widgetClick WidgetClickHandler
	// qr login handler
	qrLogin QRLoginHandler
}

func NewPrompter(hub *Hub) *Prompter {
	p := &Prompter{
		hub:     hub,
		replies: make(map[string]chan any),
	}
	hub.SetMessageHandler(p.onMessage)
	return p
}

// register widget click handler
func (p *Prompter) SetWidgetClickHandler(fn WidgetClickHandler) {
	p.mu.Lock()
	p.widgetClick = fn
	p.mu.Unlock()
}

// register qr login handler
func (p *Prompter) SetQRLoginHandler(fn QRLoginHandler) {
	p.mu.Lock()
	p.qrLogin = fn
	p.mu.Unlock()
}

func (p *Prompter) onMessage(c *Client, raw []byte) {
	var evt struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		return
	}

	if evt.Type == "ping" {
		return
	}

	// qr login event
	if evt.Type == "telegram_qr_login" {
		p.mu.Lock()
		fn := p.qrLogin
		p.mu.Unlock()
		if fn == nil {
			p.hub.Broadcast(newEvent(EvtLog, LogPayload{
				Level: "ERR", Name: "telegram",
				Msg: "QR login is not available: telegram transport is not loaded",
			}))
			return
		}
		if err := fn(); err != nil {
			p.hub.Broadcast(newEvent(EvtLog, LogPayload{Level: "ERR", Name: "telegram", Msg: err.Error()}))
		}
		return
	}

	if evt.Type == "widget_click" {
		var click struct {
			ID      string `json:"id"`
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal(evt.Data, &click); err != nil {
			return
		}
		p.mu.Lock()
		handler := p.widgetClick
		p.mu.Unlock()
		if handler == nil {
			return
		}
		out := handler(click.ID, click.Payload)
		if out != "" {
			p.hub.Broadcast(newEvent(EvtWidgetData, WidgetDataPayload{
				ID:   click.ID,
				Data: out,
			}))
		}
		return
	}

	// prompt reply event
	if evt.Type != EvtPromptReply {
		return
	}
	var reply PromptReplyPayload
	if err := json.Unmarshal(evt.Data, &reply); err != nil {
		return
	}
	p.mu.Lock()
	ch, ok := p.replies[reply.ID]
	if ok {
		delete(p.replies, reply.ID)
	}
	p.mu.Unlock()
	if ok {
		select {
		case ch <- reply.Value:
		default:
		}
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (p *Prompter) waitForReply(id string, timeout time.Duration) (any, error) {
	ch := make(chan any, 1)
	p.mu.Lock()
	p.replies[id] = ch
	p.mu.Unlock()

	select {
	case v := <-ch:
		return v, nil
	case <-time.After(timeout):
		p.mu.Lock()
		delete(p.replies, id)
		p.mu.Unlock()
		return nil, errors.New("webui prompt: timeout")
	}
}

func (p *Prompter) hasClients() bool {
	return p.hub.Count() > 0
}

func (p *Prompter) Text(question, def string) (string, error) {
	if !p.hasClients() {
		return "", errors.New("webui: no browser connected")
	}
	id := newID()
	p.hub.Broadcast(newEvent(EvtPrompt, PromptPayload{
		ID:       id,
		Kind:     "text",
		Question: question,
		Default:  def,
	}))
	v, err := p.waitForReply(id, 5*time.Minute)
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	if s == "" {
		s = def
	}
	return s, nil
}

func (p *Prompter) YesNo(question string, def bool) (bool, error) {
	if !p.hasClients() {
		return def, errors.New("webui: no browser connected")
	}
	id := newID()
	p.hub.Broadcast(newEvent(EvtPrompt, PromptPayload{
		ID:       id,
		Kind:     "yesno",
		Question: question,
		Default:  def,
	}))
	v, err := p.waitForReply(id, 5*time.Minute)
	if err != nil {
		return def, err
	}
	b, ok := v.(bool)
	if !ok {
		return def, nil
	}
	return b, nil
}

func (p *Prompter) Select(question string, options []string, def int) (int, error) {
	if !p.hasClients() {
		return def, errors.New("webui: no browser connected")
	}
	id := newID()
	p.hub.Broadcast(newEvent(EvtPrompt, PromptPayload{
		ID:       id,
		Kind:     "select",
		Question: question,
		Default:  def,
		Options:  options,
	}))
	v, err := p.waitForReply(id, 5*time.Minute)
	if err != nil {
		return def, err
	}
	if f, ok := v.(float64); ok {
		n := int(f)
		if n >= 0 && n < len(options) {
			return n, nil
		}
	}
	return def, nil
}

// Instance
var (
	mu       sync.RWMutex
	instance *Instance
)

type Instance struct {
	cfg        Config
	hub        *Hub
	server     *Server
	prompter   *Prompter
	logFn      func(format string, args ...any)
	snapshotFn func() Snapshot
}

func Start(cfg Config, snapshotFn func() Snapshot, logFn func(string, ...any)) (*Instance, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if logFn == nil {
		logFn = func(format string, args ...any) {}
	}

	hub := NewHub()
	inst := &Instance{
		cfg:        cfg,
		hub:        hub,
		logFn:      logFn,
		snapshotFn: snapshotFn,
	}

	provider := &snapshotProvider{fn: snapshotFn}
	server := NewServer(cfg, hub, provider)
	inst.cfg = server.cfg
	if err := server.Start(); err != nil {
		return nil, err
	}
	inst.server = server
	inst.prompter = NewPrompter(hub)

	logFn("listening on http://%s:%d", server.cfg.Bind, server.cfg.Port)
	logFn("dashboard url: http://%s:%d/?token=%s", server.cfg.Bind, server.cfg.Port, server.cfg.AuthToken)

	mu.Lock()
	instance = inst
	mu.Unlock()
	return inst, nil
}

func Global() *Instance {
	mu.RLock()
	defer mu.RUnlock()
	return instance
}

func (i *Instance) Stop() {
	if i == nil || i.server == nil {
		return
	}
	i.server.Stop()
	i.logFn("stopped")
}

func (i *Instance) Hub() *Hub {
	if i == nil {
		return nil
	}
	return i.hub
}

func (i *Instance) Prompter() *Prompter {
	if i == nil {
		return nil
	}
	return i.prompter
}

func (i *Instance) URL() string {
	if i == nil || i.server == nil || i.server.ln == nil {
		return ""
	}
	base := "http://" + i.server.ln.Addr().String()
	if i.cfg.AuthToken != "" {
		return base + "/?token=" + i.cfg.AuthToken
	}
	return base
}

func (i *Instance) Config() Config {
	if i == nil {
		return Config{}
	}
	return i.cfg
}

// core event emission wrappers
func (i *Instance) Log(level, name, msg string) {
	if i == nil || i.hub == nil {
		return
	}
	i.hub.Broadcast(newEvent(EvtLog, LogPayload{
		Level: level, Name: name, Msg: msg,
	}))
}

func (i *Instance) AuthEvent(eventJSON string) {
	if i == nil || i.hub == nil {
		return
	}
	var event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
		return
	}
	if event.Type != "telegram_qr" && event.Type != "telegram_auth_state" {
		return
	}
	var data any
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return
	}
	evt := newEvent(event.Type, data)
	if event.Type == "telegram_qr" {
		i.hub.setAuthEvent(evt)
	} else if event.Type == "telegram_auth_state" {
		// ignore intermediate qr login states
		var st struct {
			State string `json:"state"`
		}
		if json.Unmarshal(event.Data, &st) == nil &&
			(st.State == "connected" || st.State == "disconnected") {
			i.hub.clearAuthEvent()
		}
	}
	i.hub.Broadcast(evt)
}

func (i *Instance) Metrics(queue, active, errors int64) {
	if i == nil || i.hub == nil {
		return
	}
	i.hub.Broadcast(newEvent(EvtMetrics, MetricsPayload{
		QueueLen: queue, Active: active, Errors: errors,
	}))
}

func (i *Instance) Stats(messages, toolCalls, replies int64) {
	if i == nil || i.hub == nil {
		return
	}
	i.hub.Broadcast(newEvent(EvtStats, StatsPayload{
		Messages: messages, ToolCalls: toolCalls, Replies: replies,
	}))
}

func (i *Instance) Transport(name, kind, status string) {
	if i == nil || i.hub == nil {
		return
	}
	i.hub.Broadcast(newEvent(EvtTransport, TransportPayload{
		Name: name, Kind: kind, Status: status,
	}))
}

func (i *Instance) PluginList(plugins []PluginPayload) {
	if i == nil || i.hub == nil {
		return
	}
	i.hub.Broadcast(newEvent(EvtPlugin, plugins))
}

// streaming wrappers
func (i *Instance) StreamStart(chatID int64, senderName string) string {
	if i == nil || i.hub == nil {
		return ""
	}
	id := fmt.Sprintf("s_%d_%d", chatID, time.Now().UnixNano())
	i.hub.Broadcast(newEvent(EvtStreamStart, StreamStartPayload{
		ID:         id,
		ChatID:     chatID,
		SenderName: senderName,
		TS:         time.Now().UnixMilli(),
	}))
	return id
}

func (i *Instance) StreamToken(id, delta string) {
	if i == nil || i.hub == nil || id == "" || delta == "" {
		return
	}
	i.hub.Broadcast(newEvent(EvtStreamToken, StreamTokenPayload{
		ID:    id,
		Delta: delta,
	}))
}

func (i *Instance) StreamEnd(id, answer string, err error) {
	if i == nil || i.hub == nil || id == "" {
		return
	}
	p := StreamEndPayload{ID: id, Answer: answer}
	if err != nil {
		p.Error = err.Error()
	}
	i.hub.Broadcast(newEvent(EvtStreamEnd, p))
}

// push widget data update
func (i *Instance) WidgetData(id, dataJSON string) {
	if i == nil || i.hub == nil || id == "" {
		return
	}
	i.hub.Broadcast(newEvent(EvtWidgetData, WidgetDataPayload{
		ID:   id,
		Data: dataJSON,
	}))
}

// push toast notification
func (i *Instance) WidgetNotify(id string, level int, msg string, durationMs uint32) {
	if i == nil || i.hub == nil {
		return
	}
	i.hub.Broadcast(newEvent(EvtWidgetNotify, WidgetNotifyPayload{
		ID:         id,
		Level:      level,
		Msg:        msg,
		DurationMs: durationMs,
	}))
}

type snapshotProvider struct {
	fn func() Snapshot
}

func (s *snapshotProvider) Snapshot() Snapshot {
	if s.fn == nil {
		return Snapshot{}
	}
	return s.fn()
}

func getBool(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func getString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func getInt(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}
