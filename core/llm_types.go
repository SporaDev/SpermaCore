package main

import (
	"context"
	"errors"
)

// errModelNoTools indicates model does not support tool calling
var errModelNoTools = errors.New("model does not support tools")

// universal chat message structure
type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Images     []string       `json:"images,omitempty"`
	Videos     []string       `json:"videos,omitempty"`
	Audios     []string       `json:"audios,omitempty"`
	Thinking   string         `json:"thinking,omitempty"`
	ToolCalls  []ChatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type ChatToolCall struct {
	ID       string       `json:"id,omitempty"`
	Type     string       `json:"type,omitempty"`
	Function ChatFunction `json:"function"`
}

type ChatFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// stream chunk payload
type StreamChunk struct {
	Delta     string
	ToolCalls []ChatToolCall
	Done      bool
	Err       error
}

// synchronous provider interface
type LLMProvider interface {
	Chat(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (ChatMessage, error)
	Name() string
}

// streaming provider interface
type StreamingProvider interface {
	LLMProvider
	ChatStream(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) <-chan StreamChunk
}
