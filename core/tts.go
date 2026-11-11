package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultTTSPrompt prepares a reply for speech synthesis. It is deliberately
// separate from the chat prompt: the model keeps its personality when writing,
// but the spoken version must be clean, natural and free of markup.
const defaultTTSPrompt = `Ты - редактор текста для озвучки (text-to-speech).
Преобразуй сообщение в текст, который естественно звучит вслух.
Правила:
- сохрани язык, смысл, эмоции и обращение к собеседнику;
- убери markdown, эмодзи, смайлы, ссылки, хэштеги, списки и ремарки в скобках;
- не добавляй ничего от себя, не комментируй задачу и не пиши пояснений;
- расставь естественные паузы обычными знаками препинания;
- верни только готовый текст для озвучки, без кавычек и префиксов.`

// ttsSpeakableText turns a chat reply into clean text for synthesis. It strips
// markup locally and (optionally) rewrites the text with the TTS prompt.
func ttsSpeakableText(cfg *Config, text string) string {
	text = sanitizeTTSText(text)
	if text == "" {
		return ""
	}
	if cfg == nil || !cfg.TTS.ShouldRewrite() {
		return text
	}
	prov := GetGlobalProvider()
	if prov == nil {
		return text
	}
	prompt := strings.TrimSpace(cfg.TTS.Prompt)
	if prompt == "" {
		prompt = defaultTTSPrompt
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TTS.TimeoutSecs)*time.Second)
	defer cancel()
	model := cfg.ModelNameFor("tts")
	resp, err := prov.Chat(ctx, model, []ChatMessage{
		{Role: "system", Content: prompt},
		{Role: "user", Content: text},
	}, nil)
	if err != nil {
		return text
	}
	out := sanitizeTTSText(resp.Content)
	if out == "" {
		return text
	}
	return out
}

// sanitizeTTSText removes constructs that sound bad when read aloud.
func sanitizeTTSText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var b strings.Builder
	inCode := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		if inCode {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#>+-*• \t")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		b.WriteString(trimmed)
	}
	out := b.String()
	// Drop code spans and bare URLs: both are unreadable out loud.
	for {
		start := strings.Index(out, "`")
		if start < 0 {
			break
		}
		end := strings.Index(out[start+1:], "`")
		if end < 0 {
			break
		}
		out = out[:start] + out[start+1+end+1:]
	}
	out = stripURLs(out)
	// Drop markdown emphasis markers that would be read as stray characters.
	out = strings.NewReplacer("**", "", "__", "", "~~", "", "*", "", "_", "").Replace(out)
	// Remove emoji and pictographs, then collapse whitespace.
	out = strings.Map(func(r rune) rune {
		switch {
		case r >= 0x1F000 && r <= 0x1FAFF, // emoji & symbols
			r >= 0x2600 && r <= 0x27BF, // misc symbols / dingbats
			r >= 0xFE00 && r <= 0xFE0F, // variation selectors
			r == 0x200D:                // zero-width joiner
			return -1
		}
		return r
	}, out)
	out = strings.Join(strings.Fields(out), " ")
	return strings.TrimSpace(out)
}

// stripURLs removes http(s) links but keeps the surrounding words.
func stripURLs(s string) string {
	fields := strings.Fields(s)
	out := fields[:0]
	for _, f := range fields {
		lower := strings.ToLower(f)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") ||
			strings.HasPrefix(lower, "www.") || strings.HasPrefix(lower, "t.me/") {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// truncateTTS limits text by runes so the provider is not overloaded.
func truncateTTS(text string, maxChars int) string {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	runes := []rune(text)
	cut := string(runes[:maxChars])
	if idx := strings.LastIndexAny(cut, ".!?…"); idx > maxChars/2 {
		cut = cut[:idx+1]
	}
	return strings.TrimSpace(cut) + "…"
}

type ttsResult struct {
	Audio []byte
	Ext   string
}

// synthesizeSpeech calls the configured TTS provider and returns raw audio.
func synthesizeSpeech(cfg *Config, text string) (*ttsResult, error) {
	if cfg == nil {
		return nil, errors.New("tts: config is not loaded")
	}
	if !cfg.TTS.IsEnabled() {
		return nil, errors.New("tts: disabled in config")
	}
	text = truncateTTS(strings.TrimSpace(text), cfg.TTS.MaxChars)
	if text == "" {
		return nil, errors.New("tts: empty text")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TTS.TimeoutSecs)*time.Second)
	defer cancel()

	switch strings.ToLower(strings.TrimSpace(cfg.TTS.Provider)) {
	case "", "fish", "fishaudio", "fish_audio":
		return fishAudioSynthesize(ctx, cfg, text)
	case "openai":
		return openAISpeechSynthesize(ctx, cfg, text)
	default:
		return nil, fmt.Errorf("tts: unknown provider %q", cfg.TTS.Provider)
	}
}

func ttsFormat(cfg *Config) string {
	f := strings.ToLower(strings.TrimSpace(cfg.TTS.Format))
	switch f {
	case "ogg", "opus", "oga":
		return "opus"
	case "mp3", "wav", "pcm":
		return f
	default:
		return "opus"
	}
}

func ttsExt(format string) string {
	switch format {
	case "opus", "ogg", "oga":
		return "ogg"
	case "mp3":
		return "mp3"
	case "wav":
		return "wav"
	case "pcm":
		return "pcm"
	default:
		return "ogg"
	}
}

func fishAudioSynthesize(ctx context.Context, cfg *Config, text string) (*ttsResult, error) {
	format := ttsFormat(cfg)
	payload := map[string]any{
		"text":         text,
		"format":       format,
		"normalize":    true,
		"latency":      "normal",
		"chunk_length": 200,
	}
	if cfg.TTS.Voice != "" {
		payload["reference_id"] = cfg.TTS.Voice
	}
	if cfg.TTS.Speed > 0 && cfg.TTS.Speed != 1.0 || cfg.TTS.Volume != 0 {
		prosody := map[string]any{}
		if cfg.TTS.Speed > 0 {
			prosody["speed"] = cfg.TTS.Speed
		}
		if cfg.TTS.Volume != 0 {
			prosody["volume"] = cfg.TTS.Volume
		}
		payload["prosody"] = prosody
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(cfg.TTS.BaseURL, "/")
	endpoint := base + "/v1/tts"
	if strings.HasSuffix(base, "/v1") {
		endpoint = base + "/tts"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.TTS.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.TTS.APIKey)
	}
	if cfg.TTS.Model != "" {
		req.Header.Set("model", cfg.TTS.Model)
	}
	audio, err := doTTSRequest(req)
	if err != nil {
		return nil, fmt.Errorf("fish audio: %w", err)
	}
	return &ttsResult{Audio: audio, Ext: ttsExt(format)}, nil
}

func openAISpeechSynthesize(ctx context.Context, cfg *Config, text string) (*ttsResult, error) {
	format := ttsFormat(cfg)
	voice := cfg.TTS.Voice
	if voice == "" {
		voice = "alloy"
	}
	payload := map[string]any{
		"model":           cfg.TTS.Model,
		"input":           text,
		"voice":           voice,
		"response_format": format,
	}
	if cfg.TTS.Speed > 0 && cfg.TTS.Speed != 1.0 {
		payload["speed"] = cfg.TTS.Speed
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(cfg.TTS.BaseURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/audio/speech", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.TTS.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.TTS.APIKey)
	}
	audio, err := doTTSRequest(req)
	if err != nil {
		return nil, fmt.Errorf("openai tts: %w", err)
	}
	return &ttsResult{Audio: audio, Ext: ttsExt(format)}, nil
}

func doTTSRequest(req *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(audio) == 0 {
		return nil, errors.New("provider returned empty audio")
	}
	return audio, nil
}

// synthesizeVoiceNote renders text to speech and reports the resulting audio
// duration in seconds (derived from the provider's audio, not from the text
// length). The returned path points to a file inside <data_dir>/tts.
func synthesizeVoiceNote(cfg *Config, text string) (string, float64, error) {
	res, err := synthesizeSpeech(cfg, text)
	if err != nil {
		return "", 0, err
	}
	dataDir := "./data"
	if cfg != nil && cfg.Core.DataDir != "" {
		dataDir = cfg.Core.DataDir
	}
	dir := filepath.Join(dataDir, "tts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("tts: create dir: %w", err)
	}
	cleanupOldTTSFiles(dir, 24*time.Hour)

	ext := detectAudioExt(res.Audio, res.Ext)
	name := fmt.Sprintf("tts-%d.%s", time.Now().UnixNano(), ext)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, res.Audio, 0o644); err != nil {
		return "", 0, fmt.Errorf("tts: write audio: %w", err)
	}
	seconds := audioDurationSeconds(res.Audio)
	if seconds <= 0 {
		seconds = estimateSpeechSeconds(text)
	}
	return path, seconds, nil
}

// cleanupOldTTSFiles drops cached audio older than maxAge. Synthesis happens
// rarely and Telegram needs the file only briefly, so keeping a day is plenty.
func cleanupOldTTSFiles(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// detectAudioExt prefers the real container signature over the requested
// format, because providers sometimes fall back to a different container.
func detectAudioExt(data []byte, fallback string) string {
	switch {
	case len(data) >= 4 && string(data[:4]) == "OggS":
		return "ogg"
	case len(data) >= 3 && string(data[:3]) == "ID3":
		return "mp3"
	case len(data) >= 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "mp3"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE":
		return "wav"
	}
	return fallback
}

// audioDurationSeconds extracts the duration of the returned audio. OGG/Opus
// granule positions and WAV headers carry the exact length; other containers
// fall back to zero so the caller can estimate.
func audioDurationSeconds(data []byte) float64 {
	if len(data) >= 4 && string(data[:4]) == "OggS" {
		if d, ok := oggDurationSeconds(data); ok {
			return d
		}
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		if d, ok := wavDurationSeconds(data); ok {
			return d
		}
	}
	return 0
}

// oggDurationSeconds reads the granule position of the final Ogg page. For
// Opus the granule is the number of 48 kHz samples. This is exactly what the
// provider produced, so the voice note length matches the actual audio.
func oggDurationSeconds(data []byte) (float64, bool) {
	idx := bytes.LastIndex(data, []byte("OggS"))
	if idx < 0 || idx+14 > len(data) {
		return 0, false
	}
	granule := binary.LittleEndian.Uint64(data[idx+6 : idx+14])
	if granule == 0 || granule == ^uint64(0) {
		return 0, false
	}
	return float64(granule) / 48000.0, true
}

func wavDurationSeconds(data []byte) (float64, bool) {
	if len(data) < 44 {
		return 0, false
	}
	byteRate := binary.LittleEndian.Uint32(data[28:32])
	if byteRate == 0 {
		return 0, false
	}
	// Data chunk size is usually at offset 40, but walk chunks to be safe.
	for off := 12; off+8 <= len(data); {
		id := string(data[off : off+4])
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		if id == "data" {
			if size <= 0 || off+8+size > len(data) {
				size = len(data) - off - 8
			}
			return float64(size) / float64(byteRate), true
		}
		off += 8 + size
		if size < 0 {
			break
		}
	}
	return 0, false
}

// estimateSpeechSeconds is a last-resort duration used when the container does
// not expose its length. Russian speech averages roughly 14 characters/sec.
func estimateSpeechSeconds(text string) float64 {
	n := utf8.RuneCountInString(strings.TrimSpace(text))
	if n < 20 {
		n = 20
	}
	secs := float64(n) / 14.0
	if secs < 1.5 {
		secs = 1.5
	}
	return secs
}
