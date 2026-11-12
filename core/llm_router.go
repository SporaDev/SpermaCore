package main

import (
	"context"
	"fmt"
	"strings"
)

// ProviderRouter routes requests by provider prefix or default provider
type ProviderRouter struct {
	providers map[string]LLMProvider
	defName   string
	defModel  string
	def       LLMProvider
}

type unconfiguredProvider struct {
	name   string
	reason string
}

func (u *unconfiguredProvider) Name() string { return u.name }

func (u *unconfiguredProvider) Chat(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (ChatMessage, error) {
	return ChatMessage{
		Role:    "assistant",
		Content: fmt.Sprintf("llm provider (%s) is not configured: %s. set api key in .env or config.toml.", u.name, u.reason),
	}, nil
}

func (u *unconfiguredProvider) ChatStream(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) <-chan StreamChunk {
	ch := make(chan StreamChunk, 1)
	text := fmt.Sprintf("llm provider (%s) is not configured: %s. set api key in .env or config.toml.", u.name, u.reason)
	ch <- StreamChunk{Delta: text, Done: true}
	close(ch)
	return ch
}

func (r *ProviderRouter) Name() string { return r.defName }

// resolve separates provider prefix and model name
func (r *ProviderRouter) resolve(model string) (LLMProvider, string) {
	model = strings.TrimSpace(model)
	if i := strings.IndexByte(model, '/'); i > 0 {
		name := strings.ToLower(model[:i])
		if p, ok := r.providers[name]; ok {
			return p, strings.TrimLeft(model[i+1:], "/")
		}
		// handle ollama/ prefix compatibility
		if name == "ollama" {
			return r.def, strings.TrimLeft(model[i+1:], "/")
		}
		// Not a configured provider: the leading segment is part of the model
		// id itself (e.g. "google/gemini-2.0-flash"). Forward the full string
		// to the default provider instead of silently replacing it.
		return r.def, model
	}
	return r.def, model
}

func (r *ProviderRouter) Chat(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (ChatMessage, error) {
	p, m := r.resolve(model)
	return p.Chat(ctx, m, messages, tools)
}

func (r *ProviderRouter) ChatStream(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) <-chan StreamChunk {
	p, m := r.resolve(model)
	if sp, ok := p.(StreamingProvider); ok {
		return sp.ChatStream(ctx, m, messages, tools)
	}
	out := make(chan StreamChunk, 1)
	go func() {
		defer close(out)
		msg, err := p.Chat(ctx, m, messages, tools)
		if err != nil {
			out <- StreamChunk{Err: err, Done: true}
			return
		}
		out <- StreamChunk{Delta: msg.Content, ToolCalls: msg.ToolCalls, Done: true}
	}()
	return out
}
