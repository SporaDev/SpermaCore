package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// errModelNoTools indicates model does not support tool calling
var errModelNoTools = errors.New("model does not support tools")

// universal chat message structure
type ChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Images     []string       `json:"images,omitempty"`
	Videos     []string       `json:"videos,omitempty"`
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

// Ollama
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
	// parse ollama tool arguments

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

// openai-compatible provider implementation
type OpenAIProvider struct {
	baseURL string
	apiKey  string
	client  *http.Client
	name    string
	headers map[string]string
	extra   map[string]any
}

func NewOpenAIProvider(name, baseURL, apiKey string, timeout time.Duration) *OpenAIProvider {
	return NewOpenAIProviderExt(name, baseURL, apiKey, timeout, nil, nil)
}

// new openai provider with custom headers and extra fields
func NewOpenAIProviderExt(name, baseURL, apiKey string, timeout time.Duration,
	headers map[string]string, extra map[string]any) *OpenAIProvider {
	return &OpenAIProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
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
		name:    name,
		headers: headers,
		extra:   extra,
	}
}

func (p *OpenAIProvider) Name() string { return p.name }

func (p *OpenAIProvider) Chat(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) (ChatMessage, error) {
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

func (p *OpenAIProvider) ChatStream(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any) <-chan StreamChunk {
	out := make(chan StreamChunk, 16)
	go func() {
		defer close(out)
		p.streamInternal(ctx, model, messages, tools, out)
	}()
	return out
}

func (p *OpenAIProvider) streamInternal(ctx context.Context, model string, messages []ChatMessage, tools []map[string]any, out chan<- StreamChunk) {
	rawMessages := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		var content any
		if len(m.Images) > 0 {
			parts := make([]map[string]any, 0, len(m.Images)+1)
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, img := range m.Images {
				parts = append(parts, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": "data:image/jpeg;base64," + img,
					},
				})
			}
			content = parts
		} else {
			content = m.Content
		}

		msg := map[string]any{"role": m.Role, "content": content}
		if len(m.Videos) > 0 {
			msg["videos"] = m.Videos
		}
		if len(m.ToolCalls) > 0 {
			tcs := make([]map[string]any, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				argsJSON, _ := json.Marshal(tc.Function.Arguments)
				id := tc.ID
				if id == "" {
					id = fmt.Sprintf("call_%d", time.Now().UnixNano())
				}
				tcs = append(tcs, map[string]any{
					"id":   id,
					"type": "function",
					"function": map[string]any{
						"name":      tc.Function.Name,
						"arguments": string(argsJSON),
					},
				})
			}
			msg["tool_calls"] = tcs
		}
		if m.ToolCallID != "" {
			msg["tool_call_id"] = m.ToolCallID
		}
		rawMessages = append(rawMessages, msg)
	}

	req := map[string]any{
		"model":    model,
		"messages": rawMessages,
		"stream":   true,
	}
	if len(tools) > 0 {
		req["tools"] = tools
	}
	// merge extra fields without overriding core keys
	for k, v := range p.extra {
		if _, taken := req[k]; taken {
			continue
		}
		req[k] = v
	}

	body, err := json.Marshal(req)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("encode: %w", err), Done: true}
		return
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		out <- StreamChunk{Err: err, Done: true}
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	httpReq.Header.Set("HTTP-Referer", "https://github.com/uglyaz0tix/spermacore")
	httpReq.Header.Set("X-Title", "SpermaCore")
	// append custom headers
	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		out <- StreamChunk{Err: fmt.Errorf("http: %w", err), Done: true}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		body := string(raw)
		// check for tool calling unsupported status
		lower := strings.ToLower(body)
		if strings.Contains(lower, "does not support tool") ||
			strings.Contains(lower, "tools are not supported") ||
			strings.Contains(lower, "function calling is not supported") {
			out <- StreamChunk{Err: fmt.Errorf("%w: %s", errModelNoTools, truncate(body, 300)), Done: true}
			return
		}
		out <- StreamChunk{Err: fmt.Errorf("%s http %d: %s", p.name, resp.StatusCode, truncate(body, 400)), Done: true}
		return
	}

	// parse sse lines
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// accumulate tool call chunks
	type tcAccum struct {
		id        string
		name      string
		argsAccum strings.Builder
	}
	toolAccums := map[int]*tcAccum{}

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			out <- StreamChunk{Err: ctx.Err(), Done: true}
			return
		default:
		}

		// trim sse data prefix
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}

		var parsed struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			continue
		}
		if parsed.Error != nil {
			out <- StreamChunk{Err: fmt.Errorf("%s: %s", p.name, parsed.Error.Message), Done: true}
			return
		}
		if len(parsed.Choices) == 0 {
			continue
		}

		delta := parsed.Choices[0].Delta

		// text chunk
		if delta.Content != "" {
			out <- StreamChunk{Delta: delta.Content}
		}

		// tool calls delta
		for _, tc := range delta.ToolCalls {
			acc, ok := toolAccums[tc.Index]
			if !ok {
				acc = &tcAccum{}
				toolAccums[tc.Index] = acc
			}
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				acc.argsAccum.WriteString(tc.Function.Arguments)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		out <- StreamChunk{Err: err, Done: true}
		return
	}

	// finalize tool calls list
	indices := make([]int, 0, len(toolAccums))
	for i := range toolAccums {
		indices = append(indices, i)
	}
	sort.Ints(indices)

	var finalToolCalls []ChatToolCall
	for _, i := range indices {
		acc := toolAccums[i]
		var args map[string]any
		if acc.argsAccum.Len() > 0 {
			_ = json.Unmarshal([]byte(acc.argsAccum.String()), &args)
		}
		if args == nil {
			args = map[string]any{}
		}
		finalToolCalls = append(finalToolCalls, ChatToolCall{
			ID:   acc.id,
			Type: "function",
			Function: ChatFunction{
				Name:      acc.name,
				Arguments: args,
			},
		})
	}

	out <- StreamChunk{
		ToolCalls: finalToolCalls,
		Done:      true,
	}
}

// buildProvider creates default provider from [llm]
func buildProvider(cfg *Config) (LLMProvider, string, int, error) {
	return buildProviderNamed(cfg, cfg.LLM.Provider)
}

// buildProviderNamed creates named provider
func buildProviderNamed(cfg *Config, rawName string) (LLMProvider, string, int, error) {
	name := strings.ToLower(strings.TrimSpace(rawName))
	pc, ok := cfg.LLM.Providers[name]
	if !ok {
		return nil, "", 0, fmt.Errorf("provider %q not configured in [llm.providers.%s]", name, name)
	}

	timeout := time.Duration(pc.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	maxConc := pc.MaxConcurrent
	if maxConc <= 0 {
		maxConc = 2
	}

	typ := strings.ToLower(strings.TrimSpace(pc.Type))
	if typ == "" {
		if name == "ollama" {
			typ = "ollama"
		} else {
			typ = "openai"
		}
	}

	switch typ {
	case "ollama":
		base := pc.BaseURL
		if base == "" {
			base = "http://localhost:11434"
		}
		return NewOllamaProvider(base, timeout), base, maxConc, nil
	case "openai", "openai-compatible", "custom":
		if pc.APIKey == "" && len(pc.Headers) == 0 {
			return nil, "", 0, fmt.Errorf("provider %q requires api_key (set in .env and use ${VAR} in config.toml, or configure [llm.providers.%s.headers])", name, name)
		}
		if pc.BaseURL == "" {
			return nil, "", 0, fmt.Errorf("provider %q requires base_url", name)
		}
		return NewOpenAIProviderExt(name, pc.BaseURL, pc.APIKey, timeout, pc.Headers, pc.Extra), pc.BaseURL, maxConc, nil
	default:
		return nil, "", 0, fmt.Errorf("provider %q has unsupported type %q (use openai or ollama)", name, pc.Type)
	}
}

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

func buildProviders(cfg *Config) (*ProviderRouter, string, int, error) {
	r := &ProviderRouter{
		providers: map[string]LLMProvider{},
		defModel:  cfg.LLM.Model,
	}
	var (
		defEndpoint string
		defMaxConc  int
		lastErr     error
	)
	// register configured providers
	defaultName := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))

	required := map[string]bool{defaultName: true}
	for _, role := range []string{"chat", "vision"} {
		if p := cfg.ProviderFor(role); p != "" {
			required[strings.ToLower(p)] = true
		}
	}

	for name := range cfg.LLM.Providers {
		p, endpoint, maxConc, err := buildProviderNamed(cfg, name)
		if err != nil {
			if required[name] {
				lastErr = err
				GetLogger("llm").Warn("provider %q: %v", name, err)
			}
			continue
		}
		r.providers[name] = p
		if name == defaultName {
			defEndpoint = endpoint
			defMaxConc = maxConc
		}
	}

	if def := r.providers[defaultName]; def != nil {
		r.def = def
		r.defName = defaultName
	}
	if r.def == nil {
		reason := "not configured"
		if lastErr != nil {
			reason = lastErr.Error()
		}
		GetLogger("llm").Warn("warning: default llm provider %q is not configured (%s). running in waiting mode.", defaultName, reason)
		fallback := &unconfiguredProvider{name: defaultName, reason: reason}
		r.providers[defaultName] = fallback
		r.def = fallback
		r.defName = defaultName
		defEndpoint = "not configured (waiting for api key)"
		defMaxConc = 2
	}
	return r, defEndpoint, defMaxConc, nil
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
		if r.defModel != "" {
			return r.def, r.defModel
		}
		return r.def, strings.TrimLeft(model[i+1:], "/")
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
