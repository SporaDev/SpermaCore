package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSplitForTransport(t *testing.T) {
	if got := splitForTransport("hello", 0); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("unlimited split = %#v", got)
	}
	if got := splitForTransport("hello", 10); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("short split = %#v", got)
	}
	// split limit operates on runes not bytes
	got := splitForTransport("héllo-world", 5)
	want := []string{"héllo", "-worl", "d"}
	if len(got) != len(want) {
		t.Fatalf("split = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("split[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// joining chunks preserves full string
	if strings.Join(got, "") != "héllo-world" {
		t.Fatalf("split lost data: %#v", got)
	}
}

func TestTransportPayloadIsValidJSON(t *testing.T) {
	raw := transportPayload(map[string]any{
		"chat_id":    "123",
		"message_id": "456",
		"text":       "hello",
	})
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("payload is not JSON: %v (%q)", err, raw)
	}
	if parsed["chat_id"] != "123" || parsed["text"] != "hello" {
		t.Fatalf("payload = %#v", parsed)
	}
}
