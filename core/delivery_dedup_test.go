package main

import "testing"

func TestNormalizeDeliveredText(t *testing.T) {
	cases := []struct{ a, b string }{
		{"ага, идеальна|SPLIT|пока коврик", "ага, идеальна пока коврик"},
		{"  Hello   World  ", "helloworld"},
		{"one|NEXT|two", "one two"},
	}
	for _, tc := range cases {
		if got, want := normalizeDeliveredText(tc.a), normalizeDeliveredText(tc.b); got != want {
			t.Errorf("normalizeDeliveredText(%q) = %q, want %q", tc.a, got, want)
		}
	}
}

func TestDeliveredTrackerSuppressesDuplicates(t *testing.T) {
	d := newDeliveredTracker()
	const chat = "chat:42"
	if d.repeats(chat, "anything") {
		t.Fatal("empty tracker must not report a duplicate")
	}
	d.add(chat, "первый|SPLIT|второй")
	if !d.seen(chat, "первый") || !d.seen(chat, "второй") {
		t.Fatal("split bubbles should be tracked individually")
	}
	if !d.repeats(chat, "первый|SPLIT|второй") {
		t.Fatal("the whole message must be recognised as a duplicate")
	}
	if !d.repeats(chat, "Первый | SPLIT | Второй") {
		t.Fatal("duplicate detection must ignore case and whitespace")
	}
	if d.repeats(chat, "совсем другой текст") {
		t.Fatal("unrelated text must not be treated as a duplicate")
	}
	if d.repeats("chat:99", "первый") {
		t.Fatal("the same text in another chat is not a duplicate")
	}
	if d.empty() {
		t.Fatal("tracker should not be empty after a delivery")
	}
}

func TestMessageToolParts(t *testing.T) {
	calls, actions, ok := messageToolParts("send_message", map[string]any{"chat_id": 1, "text": "plain"})
	if !ok || len(calls) != 1 || len(actions) != 0 {
		t.Fatalf("plain text should produce a single call: ok=%t calls=%#v actions=%v", ok, calls, actions)
	}

	calls, _, ok = messageToolParts("send_message", map[string]any{"text": "one|SPLIT|two"})
	if !ok || len(calls) != 2 {
		t.Fatalf("markers should split into two calls: ok=%t calls=%#v", ok, calls)
	}

	if _, _, ok := messageToolParts("media_sticker_search", map[string]any{"query": "кот"}); ok {
		t.Fatal("non-message tools must not be treated as delivery tools")
	}

	if _, _, ok := messageToolParts("send_message", map[string]any{"text": "   "}); ok {
		t.Fatal("empty text must not be treated as a delivery")
	}
}
