package media

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRememberAndLoadEmoji(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "emoji_test_*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rec1 := EmojiRecord{
		ID:            "5368324170671202288",
		CustomEmojiID: 5368324170671202288,
		Emoji:         "🔥",
		Pack:          "Pepe Premium",
		Description:   "Pepe the frog on fire celebrating",
		File:          filepath.Join(tmpDir, "pepe_fire.webp"),
		UpdatedAt:     time.Now(),
	}

	RememberEmoji(tmpDir, rec1)

	loaded := LoadEmojis(tmpDir)
	if len(loaded) != 1 {
		t.Fatalf("expected 1 emoji, got %d", len(loaded))
	}
	if loaded[0].ID != rec1.ID || loaded[0].Emoji != "🔥" || loaded[0].Description != rec1.Description {
		t.Errorf("loaded emoji mismatch: %+v", loaded[0])
	}

	found := FindEmoji(tmpDir, "5368324170671202288")
	if found == nil || found.Emoji != "🔥" {
		t.Errorf("FindEmoji failed: %+v", found)
	}

	// Update description
	rec1.Description = "Updated Pepe fire description"
	RememberEmoji(tmpDir, rec1)

	loaded2 := LoadEmojis(tmpDir)
	if len(loaded2) != 1 {
		t.Fatalf("expected still 1 emoji after update, got %d", len(loaded2))
	}
	if loaded2[0].Description != "Updated Pepe fire description" {
		t.Errorf("description was not updated: %s", loaded2[0].Description)
	}
}
