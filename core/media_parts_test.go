package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMediaMimeTypeForPath(t *testing.T) {
	cases := map[string]string{
		"a.mp4":  "video/mp4",
		"a.webm": "video/webm",
		"a.gif":  "image/gif",
		"a.ogg":  "audio/ogg",
		"a.opus": "audio/opus",
		"a.webp": "image/webp",
		"a.tgs":  "application/octet-stream",
		"a.jpg":  "image/jpeg",
	}
	for path, want := range cases {
		if got := mediaMimeTypeForPath(path); got != want {
			t.Errorf("mediaMimeTypeForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestFileToDataURLAndAudioBase64(t *testing.T) {
	dir := t.TempDir()
	vid := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(vid, []byte("vid"), 0o644); err != nil {
		t.Fatal(err)
	}
	url, ok := fileToDataURL(vid)
	if !ok || !strings.HasPrefix(url, "data:video/mp4;base64,") {
		t.Fatalf("fileToDataURL = %q ok=%t", url, ok)
	}

	// remote URLs pass through untouched
	if u, ok := fileToDataURL("https://example.com/a.mp4"); !ok || u != "https://example.com/a.mp4" {
		t.Fatalf("remote passthrough = %q ok=%t", u, ok)
	}

	ogg := filepath.Join(dir, "voice.oga")
	_ = os.WriteFile(ogg, []byte("aud"), 0o644)
	b64, format, ok := fileToAudioBase64(ogg)
	if !ok || format != "ogg" {
		t.Fatalf("fileToAudioBase64 format = %q ok=%t", format, ok)
	}
	if dec, _ := base64.StdEncoding.DecodeString(b64); string(dec) != "aud" {
		t.Fatalf("audio payload = %q", dec)
	}
}

func TestMediaPromptTextByModality(t *testing.T) {
	cases := map[string]string{
		"photo":            "what is in this photo?",
		"gif":              "what is happening in this video?",
		"video_msg":        "what is happening in this video message?",
		"animated_sticker": "what does this animated sticker express?",
		"voice":            "what is said in this voice message?",
	}
	for media, want := range cases {
		if got := mediaPromptText(transportEvent{MediaType: media}); got != want {
			t.Errorf("mediaPromptText(%q) = %q, want %q", media, got, want)
		}
	}
	// existing caption wins
	if got := mediaPromptText(transportEvent{MediaType: "photo", Text: "look!"}); got != "look!" {
		t.Fatalf("caption not preserved: %q", got)
	}
}

func TestTimeHelpers(t *testing.T) {
	at := time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC)
	if h := messageTimeHeader(at, "5m ago"); !strings.Contains(h, "2026-11-10 12:30") || !strings.Contains(h, "5m ago") {
		t.Fatalf("messageTimeHeader = %q", h)
	}
	if s := stampHistory("hello", at); !strings.HasPrefix(s, "[2026-11-10 12:30] hello") {
		t.Fatalf("stampHistory = %q", s)
	}
	if humanAgo(3*time.Second) != "just now" || humanAgo(90*time.Second) != "1m ago" || humanAgo(3*time.Hour) != "3h ago" {
		t.Fatal("humanAgo values unexpected")
	}
	if !isTgsDocument("x.TGS") || isTgsDocument("x.webm") {
		t.Fatal("isTgsDocument mismatch")
	}
}

func TestEmbeddingLabel(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"photo":     "[photo]",
		"video":     "[video]",
		"gif":       "[video]",
		"voice":     "[voice]",
		"audio":     "[audio]",
		"sticker":   "[sticker]",
		"gibberish": "[gibberish]",
	}
	for media, want := range cases {
		if got := embeddingLabel(transportEvent{MediaType: media}); got != want {
			t.Errorf("embeddingLabel(%q) = %q, want %q", media, got, want)
		}
	}
	if got := embeddingLabel(transportEvent{Text: " hi ", MediaType: "photo"}); got != "hi" {
		t.Errorf("embeddingLabel text = %q", got)
	}
}
