package main

import (
	"strings"
	"testing"
	"time"
)

// fast chunks coalesce preserving order and data
func TestTokenThrottleCoalescesWithoutLoss(t *testing.T) {
	th := newTokenThrottle(8 * time.Millisecond)
	base := time.Now()

	tokens := make([]string, 500)
	for i := range tokens {
		tokens[i] = "x"
	}
	tokens[0] = "hello"
	tokens[len(tokens)-1] = "!"

	var got strings.Builder
	sends := 0
	for i, tok := range tokens {
		// chunks arrive within single interval window
		now := base.Add(time.Duration(i) * time.Microsecond)
		if out, ok := th.push(tok, now); ok {
			got.WriteString(out)
			sends++
		}
	}
	if out, ok := th.flush(base.Add(time.Second)); ok {
		got.WriteString(out)
		sends++
	}

	if want := strings.Join(tokens, ""); got.String() != want {
		t.Fatalf("text lost or reordered: got len=%d want len=%d", got.Len(), len(want))
	}
	if sends > 2 {
		t.Fatalf("too many sends: %d (expected 1-2 for 500 chunks)", sends)
	}
}

// slow stream passes chunks individually
func TestTokenThrottlePassesSlowStream(t *testing.T) {
	th := newTokenThrottle(8 * time.Millisecond)
	base := time.Now()

	var got []string
	for i := 0; i < 3; i++ {
		now := base.Add(time.Duration(i) * 20 * time.Millisecond)
		if out, ok := th.push("a", now); ok {
			got = append(got, out)
		}
	}
	if len(got) != 3 {
		t.Fatalf("sends %d, want 3", len(got))
	}
	for _, s := range got {
		if s != "a" {
			t.Fatalf("unexpected delta %q", s)
		}
	}
}

// flush on empty buffer emits nothing
func TestTokenThrottleFlushEmpty(t *testing.T) {
	th := newTokenThrottle(8 * time.Millisecond)
	if _, ok := th.flush(time.Now()); ok {
		t.Fatal("flush on empty buffer emitted data")
	}
}
