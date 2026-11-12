package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ollama provider implementation
type OllamaProvider struct {
	baseURL string
	client  *http.Client
}

func NewOllamaProvider(baseURL string, timeout time.Duration) *OllamaProvider {
	return &OllamaProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   30 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
			},
		},
	}
}

func (p *OllamaProvider) Name() string { return "ollama" }

// synchronous chat implementation
func (p *OllamaProvider) Chat(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (ChatMessage, error) {
	ch := p.ChatStream(ctx, model, messages, tools)
	var final ChatMessage
	for chunk := range ch {
		if chunk.Err != nil {
			return ChatMessage{}, chunk.Err
		}
		final.Content += chunk.Delta
		if len(chunk.ToolCalls) > 0 {
			final.ToolCalls = chunk.ToolCalls
		}
		if chunk.Done {
			break
		}
	}
	final.Role = "assistant"
	return final, nil
}

// stream chunks channel
func (p *OllamaProvider) ChatStream(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) <-chan StreamChunk {
	out := make(chan StreamChunk, 16)
	go func() {
		defer close(out)
		p.streamInternal(ctx, model, messages, tools, out)
	}()
	return out
}

func (p *OllamaProvider) streamInternal(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any, out chan<- StreamChunk) {
	// assemble messages
	ollamaMessages := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.Images) > 0 {
			msg["images"] = m.Images
		}
		if len(m.Videos) > 0 {
			msg["videos"] = m.Videos
		}
		if len(m.Audios) > 0 {
			msg["audios"] = m.Audios
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", time.Now().UnixNano())
				}
				tcs = append(tcs, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Function.Name,
						"arguments": tc.Function.Arguments,
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		ollamaMessages = append(ollamaMessages, msg)
	}

	req := map[string]any{
		"model":    model,
		"messages": ollamaMessages,
		"stream":   true,
	}
	if len(tools) > 0 {
		req["tools"] = tools
	}

	body, err := json.Marshal(req)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("encode: %w", err), Done: true}
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		out <- StreamChunk{Err: err, Done: true}
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("http: %w", err), Done: true}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(raw), "does not support tools") {
			out <- StreamChunk{Err: fmt.Errorf("%w: %s", errModelNoTools, truncate(string(raw), 300)), Done: true}
			return
		}
		out <- StreamChunk{Err: fmt.Errorf("ollama http %d: %s", resp.StatusCode, truncate(string(raw), 300)), Done: true}
		return
	}

	// parse ndjson stream
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var fullText strings.Builder
	var toolCalls []ChatToolCall

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- StreamChunk{Err: ctx.Err(), Done: true}
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var parsed struct {
			Message struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			Done  bool   `json:"done"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue // invalid line - skip
		}
		if parsed.Error != "" {
			out <- StreamChunk{Err: fmt.Errorf("ollama: %s", parsed.Error), Done: true}
			return
		}

		// text chunk
		if parsed.Message.Content != "" {
			fullText.WriteString(parsed.Message.Content)
			out <- StreamChunk{Delta: parsed.Message.Content}
		}

		// tool calls chunk
		if len(parsed.Message.ToolCalls) > 0 {
			for _, tc := range parsed.Message.ToolCalls {
				toolCalls = append(toolCalls, ChatToolCall{
					ID: tc.ID, Type: tc.Type,
					Function: ChatFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}

		if parsed.Done {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		out <- StreamChunk{Err: err, Done: true}
		return
	}

	// final chunk with tool calls and completion
	out <- StreamChunk{
		ToolCalls: toolCalls,
		Done:      true,
	}
}
