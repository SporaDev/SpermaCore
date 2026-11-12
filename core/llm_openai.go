package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OpenAI-compatible provider implementation
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
		if len(m.Images) > 0 || len(m.Videos) > 0 || len(m.Audios) > 0 {
			parts := make([]map[string]any, 0, len(m.Images)+len(m.Videos)+len(m.Audios)+1)
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
			for _, vid := range m.Videos {
				if url, ok := fileToDataURL(vid); ok {
					parts = append(parts, map[string]any{
						"type":      "video_url",
						"video_url": map[string]any{"url": url},
					})
				}
			}
			for _, aud := range m.Audios {
				if b64, format, ok := fileToAudioBase64(aud); ok {
					parts = append(parts, map[string]any{
						"type": "input_audio",
						"input_audio": map[string]any{
							"data":   b64,
							"format": format,
						},
					})
				}
			}
			content = parts
		} else {
			content = m.Content
		}

		msg := map[string]any{"role": m.Role, "content": content}
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

// mediaMimeTypeForPath maps a file extension to a MIME type for multimodal
// content parts (video, image, audio).
func mediaMimeTypeForPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".gif":
		return "image/gif"
	case ".ogg", ".oga":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// fileToDataURL reads a local media file and encodes it as a data URL. Remote
// URLs and already-encoded data URLs are passed through untouched.
func fileToDataURL(path string) (string, bool) {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "data:") {
		return path, true
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return "data:" + mediaMimeTypeForPath(path) + ";base64," + base64.StdEncoding.EncodeToString(data), true
}

// fileToAudioBase64 reads a local audio file and returns base64 plus the short
// format token expected by OpenAI-compatible input_audio content parts.
func fileToAudioBase64(path string) (string, string, bool) {
	if strings.HasPrefix(path, "data:") {
		if i := strings.IndexByte(path, ','); i > 0 {
			return path[i+1:], "wav", true
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if ext == "oga" {
		ext = "ogg"
	}
	if ext == "" {
		ext = "wav"
	}
	return base64.StdEncoding.EncodeToString(data), ext, true
}
