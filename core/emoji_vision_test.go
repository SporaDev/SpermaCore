package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type mockVisionProvider struct {
	lastModel   string
	lastMessage []ChatMessage
	reply       string
}

func (m *mockVisionProvider) Name() string { return "mock" }

func (m *mockVisionProvider) Chat(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (ChatMessage, error) {
	m.lastModel = model
	m.lastMessage = messages
	return ChatMessage{Role: "assistant", Content: m.reply}, nil
}

func (m *mockVisionProvider) ChatStream(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (<-chan StreamChunk, error) {
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{Delta: m.reply, Done: true}
	close(ch)
	return ch, nil
}

func TestDescribeEmoji(t *testing.T) {
	tmpDir := t.TempDir()
	dummyImg := filepath.Join(tmpDir, "emoji.webp")
	if err := os.WriteFile(dummyImg, []byte("fake webp data"), 0644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	provider := &mockVisionProvider{
		reply: "Pepe celebrating victory with a glass of champagne",
	}
	cfg := &Config{}
	cfg.LLM.Model = "gpt-4o"
	logger := GetLogger("test")

	desc := describeEmoji(context.Background(), dummyImg, "🎉", "Pepe Winners", provider, cfg, logger)
	if desc != "Pepe celebrating victory with a glass of champagne" {
		t.Errorf("unexpected description: %s", desc)
	}

	if len(provider.lastMessage) != 2 {
		t.Fatalf("expected 2 messages (system, user), got %d", len(provider.lastMessage))
	}
	if len(provider.lastMessage[1].Images) != 1 {
		t.Errorf("expected 1 attached image in user message")
	}
}
