package main

import (
	"strings"
	"testing"
	"time"
)

func resetChatContext(t *testing.T) {
	t.Helper()
	chatContextMu.Lock()
	chatContextCfg = chatContextConfig{Enabled: true, MaxMessages: 12, MaxChars: 240}
	chatContextData = map[int64][]recentLine{}
	chatContextMu.Unlock()
	t.Cleanup(func() {
		chatContextMu.Lock()
		chatContextData = map[int64][]recentLine{}
		chatContextMu.Unlock()
	})
}

func TestRecentChatTranscript(t *testing.T) {
	resetChatContext(t)
	chatID := int64(-1001)

	rememberIncomingMessage(transportEvent{ChatID: chatID, MessageID: 1, SenderName: "Ivan", SenderUsername: "ivan", Text: "hello"})
	rememberIncomingMessage(transportEvent{ChatID: chatID, MessageID: 2, SenderName: "Peter", SenderUsername: "peter", Text: "current message"})
	rememberOutgoingMessage(transportEvent{ChatID: chatID}, "bot reply")

	got := recentChatTranscript(chatID, 2)
	if strings.Contains(got, "current message") {
		t.Fatalf("current message leaked into transcript: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "bot reply") {
		t.Fatalf("transcript lost context: %q", got)
	}
	if !strings.Contains(got, "you: bot reply") {
		t.Fatalf("outgoing message not marked as self: %q", got)
	}
}

func TestLoadChatContextConfig(t *testing.T) {
	resetChatContext(t)

	loadChatContextConfig(map[string]any{
		"enabled":         false,
		"recent_messages": int64(3),
		"max_chars":       int64(50),
		"max_age_minutes": int64(2),
	})
	chatContextMu.Lock()
	enabled := chatContextCfg.Enabled
	cfg := chatContextCfg
	chatContextMu.Unlock()
	if enabled || cfg.MaxMessages != 3 || cfg.MaxChars != 50 || cfg.MaxAge != 2*time.Minute {
		t.Fatalf("config = %+v", cfg)
	}

	// disabled context stores nothing
	rememberIncomingMessage(transportEvent{ChatID: 1, MessageID: 1, Text: "ignored"})
	chatContextMu.Lock()
	n := len(chatContextData)
	chatContextMu.Unlock()
	if n != 0 {
		t.Fatalf("disabled context stored %d chats", n)
	}

	// non-positive values preserve defaults
	loadChatContextConfig(map[string]any{
		"recent_messages": 0,
		"max_chars":       -5,
		"max_age_minutes": 0,
	})
	chatContextMu.Lock()
	cfg = chatContextCfg
	chatContextMu.Unlock()
	if cfg.MaxMessages != 12 || cfg.MaxChars != 240 || cfg.MaxAge != 6*time.Hour {
		t.Fatalf("defaults = %+v", cfg)
	}

	// nil section returns defaults
	loadChatContextConfig(nil)
	chatContextMu.Lock()
	cfg = chatContextCfg
	chatContextMu.Unlock()
	if !cfg.Enabled || cfg.MaxMessages != 12 {
		t.Fatalf("nil section = %+v", cfg)
	}
}

func TestIncomingMediaPlaceholder(t *testing.T) {
	resetChatContext(t)
	rememberIncomingMessage(transportEvent{ChatID: 2, MessageID: 9, MediaType: "photo"})

	chatContextMu.Lock()
	lines := append([]recentLine(nil), chatContextData[2]...)
	chatContextMu.Unlock()
	if len(lines) != 1 || lines[0].Text != "[photo]" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestRecentChatDuplicateMessageIgnored(t *testing.T) {
	resetChatContext(t)
	chatID := int64(-1002)

	rememberIncomingMessage(transportEvent{ChatID: chatID, MessageID: 7, SenderName: "A", Text: "one"})
	rememberIncomingMessage(transportEvent{ChatID: chatID, MessageID: 7, SenderName: "A", Text: "duplicate"})

	chatContextMu.Lock()
	count := len(chatContextData[chatID])
	txt := chatContextData[chatID][0].Text
	chatContextMu.Unlock()
	if count != 1 {
		t.Fatalf("duplicate stored %d times", count)
	}
	if txt != "duplicate" {
		t.Fatalf("expected text to be updated to 'duplicate', got %q", txt)
	}
}

func TestRecentChatMessageEditUpdatesText(t *testing.T) {
	resetChatContext(t)
	chatID := int64(12345)

	rememberIncomingMessage(transportEvent{
		ChatID:    chatID,
		MessageID: 100,
		Text:      "old text with typo",
	})

	rememberIncomingMessage(transportEvent{
		ChatID:    chatID,
		MessageID: 100,
		Text:      "corrected text",
		IsEdit:    true,
	})

	chatContextMu.Lock()
	lines := chatContextData[chatID]
	chatContextMu.Unlock()

	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0].Text != "corrected text" {
		t.Fatalf("expected updated text, got %q", lines[0].Text)
	}
}
