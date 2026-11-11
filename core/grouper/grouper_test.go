package grouper

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func collectFlush(t *testing.T) (func(Event), <-chan Event) {
	t.Helper()
	ch := make(chan Event, 16)
	return func(e Event) { ch <- e }, ch
}

func waitEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for flush")
		return Event{}
	}
}

func TestNewDefaultsAndEnabled(t *testing.T) {
	flush, _ := collectFlush(t)
	g := New(Config{Enabled: true}, flush)
	if g.conf().Joiner != "\n" {
		t.Fatalf("default joiner = %q", g.conf().Joiner)
	}
	if !g.Enabled() {
		t.Fatal("Enabled() = false")
	}

	var nilG *Grouper
	if nilG.Enabled() {
		t.Fatal("nil grouper must be disabled")
	}
	nilG.SetConfig(Config{Enabled: true}) // no panic

	g.SetConfig(Config{Enabled: false, Joiner: " | "})
	if g.Enabled() {
		t.Fatal("Enabled() = true after disable")
	}
	if g.conf().Joiner != " | " {
		t.Fatalf("joiner = %q", g.conf().Joiner)
	}
}

func TestEventLine(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		want string
	}{
		{"empty", Event{Text: "  "}, ""},
		{"name", Event{Text: "hi", SenderName: "Alice"}, "[Alice] hi"},
		{"username", Event{Text: "hi", SenderUsername: "@bob"}, "[(@bob)] hi"},
		{"both", Event{Text: "hi", SenderName: "Alice", SenderUsername: "bob"}, "[Alice (@bob)] hi"},
		{"id", Event{Text: "hi", SenderID: 42}, "[user:42] hi"},
	}
	for _, tc := range cases {
		if got := eventLine(tc.ev); got != tc.want {
			t.Errorf("%s: eventLine = %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestAddDisabledFlushesImmediately(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: false}, flush)
	ev := Event{ChatID: -100, Text: "one"}
	g.Add(ev)
	got := waitEvent(t, ch)
	if got.Text != "one" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestGroupsOnly(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, GroupsOnly: true, Window: time.Hour, MaxWait: time.Hour}, flush)

	g.Add(Event{ChatID: 7, Text: "private"})
	if got := waitEvent(t, ch); got.Text != "private" {
		t.Fatalf("private text = %q", got.Text)
	}

	g.Add(Event{ChatID: -100, Text: "group"})
	select {
	case got := <-ch:
		t.Fatalf("group message must be buffered, got %q", got.Text)
	case <-time.After(50 * time.Millisecond):
	}
	g.FlushAll()
	if got := waitEvent(t, ch); got.Text != "[user:0] group" {
		t.Fatalf("group text = %q", got.Text)
	}
}

func TestAddCombinesByWindow(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, Window: 30 * time.Millisecond, MaxWait: time.Hour, Joiner: " + "}, flush)

	g.Add(Event{ChatID: -1, Text: "a", SenderName: "A"})
	g.Add(Event{ChatID: -1, Text: "b", SenderName: "B", ReplyToID: 5})
	g.Add(Event{ChatID: -1, Text: "", SemanticContext: "ctx"})

	ev := waitEvent(t, ch)
	if ev.Text != "[A] a + [B] b" {
		t.Fatalf("text = %q", ev.Text)
	}
	if ev.SenderName != "group participants" || ev.SenderUsername != "" || ev.ReplyToID != 0 {
		t.Fatalf("merged event metadata = %+v", ev)
	}
	if ev.SemanticContext != "ctx" {
		t.Fatalf("semantic context = %q", ev.SemanticContext)
	}
}

func TestAddResetsWindow(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, Window: 80 * time.Millisecond, MaxWait: time.Hour}, flush)

	g.Add(Event{ChatID: -2, Text: "a"})
	time.Sleep(40 * time.Millisecond)
	g.Add(Event{ChatID: -2, Text: "b"}) // resets timer
	select {
	case ev := <-ch:
		t.Fatalf("flushed too early: %q", ev.Text)
	case <-time.After(50 * time.Millisecond):
	}
	ev := waitEvent(t, ch)
	if ev.Text != "[user:0] a\n[user:0] b" {
		t.Fatalf("text = %q", ev.Text)
	}
}

func TestMaxWaitFlushes(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, Window: time.Hour, MaxWait: 20 * time.Millisecond}, flush)

	g.Add(Event{ChatID: -3, Text: "a"})
	time.Sleep(30 * time.Millisecond)
	g.Add(Event{ChatID: -3, Text: "b"})

	ev := waitEvent(t, ch)
	if !strings.Contains(ev.Text, "a") || !strings.Contains(ev.Text, "b") {
		t.Fatalf("text = %q", ev.Text)
	}
}

func TestDirectActivationSticky(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, Window: 20 * time.Millisecond, MaxWait: time.Hour}, flush)

	g.Add(Event{ChatID: -4, Text: "a", Activation: "direct"})
	g.Add(Event{ChatID: -4, Text: "b", Activation: "casual"})
	ev := waitEvent(t, ch)
	if ev.Activation != "direct" {
		t.Fatalf("activation = %q", ev.Activation)
	}
}

func TestTopicIsolationAndFlushAll(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, Window: time.Hour, MaxWait: time.Hour}, flush)

	g.Add(Event{ChatID: -5, TopicID: 1, Text: "topic1"})
	g.Add(Event{ChatID: -5, TopicID: 2, Text: "topic2"})
	g.FlushAll()

	var mu sync.Mutex
	seen := map[int]string{}
	for i := 0; i < 2; i++ {
		ev := waitEvent(t, ch)
		mu.Lock()
		seen[ev.TopicID] = ev.Text
		mu.Unlock()
	}
	if !strings.Contains(seen[1], "topic1") || !strings.Contains(seen[2], "topic2") {
		t.Fatalf("topics = %#v", seen)
	}
}

func TestFlushAllDisabled(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: false}, flush)
	g.FlushAll()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected flush: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestFlushUnknownKey(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true}, flush)
	g.flushChat(bufferKey{chatID: 123})
	select {
	case ev := <-ch:
		t.Fatalf("unexpected flush: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestGrouperPreservesReplyFromSelf(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, Window: 100 * time.Millisecond}, flush)

	// Message 1 is a reply to bot
	g.Add(Event{
		ChatID:        -10,
		SenderName:    "Vasya",
		Text:          "you are right!",
		ReplyToID:     42,
		ReplyFromSelf: true,
		Activation:    "direct",
	})

	// Message 2 is a follow-up from same or other user without explicit reply
	g.Add(Event{
		ChatID:     -10,
		SenderName: "Vasya",
		Text:       "and one more thing",
		Activation: "ambient",
	})

	g.FlushAll()
	ev := waitEvent(t, ch)
	if !ev.ReplyFromSelf {
		t.Fatal("expected ReplyFromSelf to be preserved as true")
	}
	if ev.Activation != "direct" {
		t.Fatalf("expected Activation to be preserved as 'direct', got %q", ev.Activation)
	}
	if ev.ReplyToID != 42 {
		t.Fatalf("expected ReplyToID to be preserved as 42, got %d", ev.ReplyToID)
	}
}

// Private chats (positive ids) merge consecutive messages without a sender
// prefix and without the "group participants" placeholder.
func TestPrivateChatsMergeWithoutPrefix(t *testing.T) {
	flush, ch := collectFlush(t)
	g := New(Config{Enabled: true, GroupsOnly: false, Window: 30 * time.Millisecond, MaxWait: time.Hour}, flush)

	g.Add(Event{ChatID: 7, Text: "first", SenderName: "Alice"})
	g.Add(Event{ChatID: 7, Text: "second", SenderName: "Alice"})

	ev := waitEvent(t, ch)
	if ev.Text != "first\nsecond" {
		t.Fatalf("private merged text = %q", ev.Text)
	}
	if ev.SenderName == "group participants" {
		t.Fatalf("private chat must not be labelled group participants: %+v", ev)
	}
}
