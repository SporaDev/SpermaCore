package main

import (
	"encoding/binary"
	"strings"
	"testing"
)

func TestSanitizeTTSText(t *testing.T) {
	in := "## Привет!\n**жирный** текст с эмодзи 😄 и ссылкой https://example.com\n```go\nfmt.Println(1)\n```\n`code` и ещё `inline`"
	got := sanitizeTTSText(in)
	if got == "" {
		t.Fatal("expected non-empty text")
	}
	for _, bad := range []string{"#", "**", "```", "fmt.Println", "https://", "😄", "`"} {
		if strings.Contains(got, bad) {
			t.Fatalf("sanitized text still contains %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "Привет!") || !strings.Contains(got, "жирный") {
		t.Fatalf("lost readable words: %q", got)
	}
}

func TestTruncateTTSKeepsSentenceBoundary(t *testing.T) {
	in := "Первое предложение. Второе предложение, которое явно длиннее лимита."
	got := truncateTTS(in, 30)
	if len([]rune(got)) > 31 {
		t.Fatalf("truncate too long: %q", got)
	}
	if strings.HasSuffix(got, "…") && !strings.Contains(got, ".") {
		t.Fatalf("expected a sentence boundary: %q", got)
	}
}

func TestVoiceActionText(t *testing.T) {
	parts := []string{"привет", "как дела"}

	if _, ok := voiceActionText(nil, parts); ok {
		t.Fatal("no VOICE action must not trigger voice")
	}
	text, ok := voiceActionText([]aiAction{{Name: "VOICE"}}, parts)
	if !ok || text != "привет\nкак дела" {
		t.Fatalf("bare VOICE = %q, %t", text, ok)
	}
	text, ok = voiceActionText([]aiAction{{Name: "VOICE", Value: "своя фраза"}}, parts)
	if !ok || text != "своя фраза" {
		t.Fatalf("VOICE=text = %q, %t", text, ok)
	}
	if _, ok := voiceActionText([]aiAction{{Name: "VOICE"}}, nil); ok {
		t.Fatal("empty reply must fall back to text")
	}
}

func TestOggDurationSeconds(t *testing.T) {
	// Two Ogg pages; the last one carries the granule position.
	data := make([]byte, 0, 40)
	page := func(granule uint64) []byte {
		buf := make([]byte, 14)
		copy(buf, "OggS")
		binary.LittleEndian.PutUint64(buf[6:14], granule)
		return buf
	}
	data = append(data, page(0)...)
	data = append(data, page(48000*3+24000)...) // 3.5 seconds

	got := audioDurationSeconds(data)
	if got < 3.4 || got > 3.6 {
		t.Fatalf("ogg duration = %v, want 3.5", got)
	}
}

func TestWavDurationSeconds(t *testing.T) {
	data := make([]byte, 44+32000)
	copy(data[0:4], "RIFF")
	copy(data[8:12], "WAVE")
	binary.LittleEndian.PutUint32(data[28:32], 16000) // byte rate
	copy(data[36:40], "data")
	binary.LittleEndian.PutUint32(data[40:44], 32000) // 2 seconds

	got := audioDurationSeconds(data)
	if got < 1.99 || got > 2.01 {
		t.Fatalf("wav duration = %v, want 2", got)
	}
}

func TestEstimateSpeechSeconds(t *testing.T) {
	if got := estimateSpeechSeconds(""); got < 1.5 {
		t.Fatalf("minimum duration = %v", got)
	}
	short := estimateSpeechSeconds("привет")
	long := estimateSpeechSeconds("это заметно более длинная фраза для проверки оценки длительности озвучки")
	if long <= short {
		t.Fatalf("longer text must estimate longer: %v <= %v", long, short)
	}
}

func TestTTSConfigDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.TTS.Provider != "fishaudio" {
		t.Fatalf("default provider = %q", cfg.TTS.Provider)
	}
	if cfg.TTS.BaseURL != "https://api.fish.audio" {
		t.Fatalf("default base_url = %q", cfg.TTS.BaseURL)
	}
	if cfg.TTS.Format != "opus" {
		t.Fatalf("default format = %q", cfg.TTS.Format)
	}
	if !cfg.TTS.IsEnabled() || !cfg.TTS.ShouldSendAsVoice() || !cfg.TTS.ShouldRewrite() {
		t.Fatal("TTS defaults must be enabled")
	}
}
