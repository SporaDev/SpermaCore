package humanify

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func testHumanifier(chance float64) *Humanifier {
	cfg := &Config{
		Enabled:           true,
		TypoChance:        chance,
		TypoMax:           1,
		DistractionChance: 0,
		SplitLong:         false,
	}
	return &Humanifier{cfg: cfg, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

func TestHumanizeLeavesProtectedTextAlone(t *testing.T) {
	h := testHumanifier(1)
	for _, text := range []string{
		"check https://example.com/very/long/path",
		"write @someone_here when free",
		"here is `fmt.Println(\"hello world\")`",
		"|SPLIT|marker cannot be broken",
		"ok",
	} {
		if got := h.Humanize(text); got != text {
			t.Fatalf("protected text changed:\n in: %q\nout: %q", text, got)
		}
	}
}

func TestHumanizeAddsOneTypo(t *testing.T) {
	h := testHumanifier(1)
	in := "hello how are you doing today"
	changed := false
	// allow multiple attempts due to randomness
	for i := 0; i < 20; i++ {
		out := h.Humanize(in)
		if out == in {
			continue
		}
		changed = true
		if len([]rune(out)) < len([]rune(in))-1 || len([]rune(out)) > len([]rune(in))+1 {
			t.Fatalf("typo changed length too much:\n in: %q\nout: %q", in, out)
		}
	}
	if !changed {
		t.Fatalf("20 tries and no typo at all: %q", in)
	}
}

func TestHumanizeDisabledByChance(t *testing.T) {
	h := testHumanifier(0)
	in := "hello how are you doing today"
	if got := h.Humanize(in); got != in {
		t.Fatalf("zero chance still changed text: %q", got)
	}
}

func TestHumanizeNoMoreThanMax(t *testing.T) {
	h := testHumanifier(1)
	h.cfg.TypoMax = 1
	in := "hello how are you doing today everything is good"
	out := h.Humanize(in)
	if strings.Fields(out)[0] == "" {
		t.Fatal("first word disappeared")
	}
}
