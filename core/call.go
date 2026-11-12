package main

// Voice "call" mode.
//
// TDLib cannot originate a real 1:1 Telegram voice call (calls are handled by
// the client-side VoIP stack, not by the MTProto API). Instead SpermaCore
// implements a *call session*: the bot "picks up" with a spoken greeting,
// transcribes every incoming voice message with Whisper and answers with a
// voice note until the caller hangs up or the session times out. From the
// user's point of view the chat behaves like a phone call.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CallConfig controls the voice-call session and its speech recognition.
type CallConfig struct {
	Enabled *bool `toml:"enabled"`
	// model is the speech-to-text model (OpenAI-compatible transcription API).
	Model string `toml:"model"`
	// provider selects which [llm.providers.*] entry supplies base_url/api_key
	// when they are not set explicitly.
	Provider string `toml:"provider"`
	BaseURL  string `toml:"base_url"`
	APIKey   string `toml:"api_key"`
	Language string `toml:"language"`
	// max_minutes caps a single call session.
	MaxMinutes int `toml:"max_minutes"`
	// greeting/farewell are spoken when a call starts/ends. Empty means the
	// built-in Russian lines are used.
	Greeting string `toml:"greeting"`
	Farewell string `toml:"farewell"`
}

// IsEnabled reports whether call mode is available (default: true).
func (c CallConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

func (c *CallConfig) applyDefaults() {
	if c.Model == "" {
		c.Model = "openai/whisper-large-v3-turbo"
	}
	if c.Provider == "" {
		c.Provider = "openrouter"
	}
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	if c.BaseURL == "" && c.Provider == "openrouter" {
		c.BaseURL = "https://openrouter.ai/api/v1"
	}
	if c.Language == "" {
		c.Language = "ru"
	}
	if c.MaxMinutes <= 0 {
		c.MaxMinutes = 15
	}
	if c.Greeting == "" {
		c.Greeting = "Алло? Я слушаю."
	}
	if c.Farewell == "" {
		c.Farewell = "Ладно, до связи."
	}
}

// callConfigFor resolves the effective call config, borrowing credentials from
// the configured LLM provider when they are not set explicitly.
func callConfigFor(cfg *Config) CallConfig {
	cc := CallConfig{}
	if cfg != nil {
		cc = cfg.Call
	}
	cc.applyDefaults()
	if cfg != nil {
		if pc, ok := cfg.LLM.Providers[cc.Provider]; ok {
			if cc.BaseURL == "" {
				cc.BaseURL = pc.BaseURL
			}
			if cc.APIKey == "" {
				cc.APIKey = pc.APIKey
			}
		}
	}
	return cc
}

// sessions

type callSession struct {
	ChatID  int64
	Started time.Time
	Expires time.Time
}

var (
	callMu       sync.Mutex
	callSessions = map[int64]callSession{}
)

// startCallSession marks a chat as being in a call. It returns the expiry time.
func startCallSession(chatID int64, max time.Duration) time.Time {
	if max <= 0 {
		max = 15 * time.Minute
	}
	now := time.Now()
	exp := now.Add(max)
	callMu.Lock()
	callSessions[chatID] = callSession{ChatID: chatID, Started: now, Expires: exp}
	callMu.Unlock()
	return exp
}

// endCallSession ends a call and reports whether one was active.
func endCallSession(chatID int64) bool {
	callMu.Lock()
	_, ok := callSessions[chatID]
	delete(callSessions, chatID)
	callMu.Unlock()
	return ok
}

// callSessionActive reports whether the chat is currently in a call. Expired
// sessions are dropped lazily.
func callSessionActive(chatID int64) bool {
	callMu.Lock()
	defer callMu.Unlock()
	s, ok := callSessions[chatID]
	if !ok {
		return false
	}
	if !s.Expires.IsZero() && time.Now().After(s.Expires) {
		delete(callSessions, chatID)
		return false
	}
	return true
}

// callSessionInfo returns session details for the `call` tool.
func callSessionInfo(chatID int64) (callSession, bool) {
	callMu.Lock()
	defer callMu.Unlock()
	s, ok := callSessions[chatID]
	if !ok {
		return callSession{}, false
	}
	if !s.Expires.IsZero() && time.Now().After(s.Expires) {
		delete(callSessions, chatID)
		return callSession{}, false
	}
	return s, true
}

// handleCallTool implements the built-in `call` tool. It returns the JSON tool
// result and, when something was spoken, the text of the voice note so the
// caller can record it as already delivered.
func handleCallTool(ev transportEvent, argsJSON string, log *Logger) (result string, spoken string) {
	var args struct {
		Action string `json:"action"`
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		action = "start"
	}
	cid := args.ChatID
	if cid == 0 {
		cid = ev.ChatID
	}
	if cid != ev.ChatID {
		return `{"status":"error","reason":"only the current chat can be called"}`, ""
	}

	cfg := GetGlobalConfig()
	cc := callConfigFor(cfg)
	if !cc.IsEnabled() {
		return `{"status":"error","reason":"call mode is disabled ([call].enabled = false)"}`, ""
	}

	switch action {
	case "start":
		if _, active := callSessionInfo(cid); active {
			return `{"status":"ok","call":"active","note":"the call is already running"}`, ""
		}
		exp := startCallSession(cid, time.Duration(cc.MaxMinutes)*time.Minute)
		line := strings.TrimSpace(args.Text)
		if line == "" {
			line = cc.Greeting
		}
		if err := deliverVoiceReply(cfg, ev, line, ev.TopicID); err != nil {
			endCallSession(cid)
			if log != nil {
				log.Warn("call: greeting failed: %v", err)
			}
			return fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error()), ""
		}
		return fmt.Sprintf(`{"status":"ok","call":"active","expires_at":%q,"note":"incoming voice messages are transcribed automatically; answer with |VOICE| so the user hears you"}`,
			exp.Format(time.RFC3339)), line

	case "end":
		wasActive := endCallSession(cid)
		line := strings.TrimSpace(args.Text)
		if line == "" {
			line = cc.Farewell
		}
		if err := deliverVoiceReply(cfg, ev, line, ev.TopicID); err != nil {
			if log != nil {
				log.Warn("call: farewell failed: %v", err)
			}
			return fmt.Sprintf(`{"status":"error","reason":%q}`, err.Error()), ""
		}
		return fmt.Sprintf(`{"status":"ok","call":"ended","was_active":%t}`, wasActive), line

	case "status":
		if s, active := callSessionInfo(cid); active {
			return fmt.Sprintf(`{"status":"ok","call":"active","started_at":%q,"expires_at":%q}`,
				s.Started.Format(time.RFC3339), s.Expires.Format(time.RFC3339)), ""
		}
		return `{"status":"ok","call":"idle"}`, ""
	}
	return fmt.Sprintf(`{"status":"error","reason":"unknown call action %q"}`, action), ""
}

// speech recognition

// transcribeAudioFile sends an audio file to an OpenAI-compatible
// /audio/transcriptions endpoint and returns the recognized text.
func transcribeAudioFile(cfg *Config, path string) (string, error) {
	cc := callConfigFor(cfg)
	if !cc.IsEnabled() {
		return "", fmt.Errorf("call: disabled")
	}
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("call: empty audio path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("call: read audio: %w", err)
	}
	name := filepath.Base(path)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return "", fmt.Errorf("call: form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("call: write form: %w", err)
	}
	_ = w.WriteField("model", cc.Model)
	if cc.Language != "" {
		_ = w.WriteField("language", cc.Language)
	}
	_ = w.WriteField("response_format", "json")
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("call: close form: %w", err)
	}

	base := strings.TrimRight(cc.BaseURL, "/")
	if base == "" {
		return "", fmt.Errorf("call: base_url is empty")
	}
	url := base + "/audio/transcriptions"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", fmt.Errorf("call: request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if cc.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cc.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("call: http: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("call: transcription failed: %s: %.200s", resp.Status, string(raw))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err == nil && strings.TrimSpace(out.Text) != "" {
		return strings.TrimSpace(out.Text), nil
	}
	// Some providers return plain text instead of JSON.
	if t := strings.TrimSpace(string(raw)); t != "" && !strings.HasPrefix(t, "{") {
		return t, nil
	}
	return "", fmt.Errorf("call: empty transcription")
}
