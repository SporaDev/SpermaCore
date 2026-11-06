package media

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type EmojiRecord struct {
	ID            string    `json:"id"`
	CustomEmojiID int64     `json:"custom_emoji_id"`
	Emoji         string    `json:"emoji,omitempty"`
	Pack          string    `json:"pack,omitempty"`
	Description   string    `json:"description"`
	File          string    `json:"file,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

var emojiStoreMu sync.Mutex

func RememberEmoji(dataDir string, r EmojiRecord) {
	if strings.TrimSpace(r.Description) == "" && strings.TrimSpace(r.ID) == "" {
		return
	}
	emojiStoreMu.Lock()
	defer emojiStoreMu.Unlock()

	paths := emojiStorePaths(dataDir)
	var all []EmojiRecord
	seen := map[string]bool{}

	for _, path := range paths {
		var items []EmojiRecord
		if b, e := os.ReadFile(path); e == nil && json.Unmarshal(b, &items) == nil {
			for _, item := range items {
				key := item.ID
				if key == "" && item.CustomEmojiID != 0 {
					key = strconv.FormatInt(item.CustomEmojiID, 10)
					item.ID = key
				}
				if key != "" && !seen[key] {
					all = append(all, item)
					seen[key] = true
				}
			}
		}
	}

	if strings.TrimSpace(r.ID) == "" {
		if r.CustomEmojiID != 0 {
			r.ID = strconv.FormatInt(r.CustomEmojiID, 10)
		} else {
			return
		}
	}

	if r.CustomEmojiID == 0 {
		if idNum, err := strconv.ParseInt(r.ID, 10, 64); err == nil {
			r.CustomEmojiID = idNum
		}
	}

	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now()
	}

	for i := range all {
		if all[i].ID == r.ID || (r.CustomEmojiID != 0 && all[i].CustomEmojiID == r.CustomEmojiID) {
			if strings.TrimSpace(r.Description) != "" {
				all[i].Description = r.Description
			}
			if r.Emoji != "" {
				all[i].Emoji = r.Emoji
			}
			if r.Pack != "" {
				all[i].Pack = r.Pack
			}
			if r.File != "" {
				all[i].File = r.File
			}
			all[i].UpdatedAt = r.UpdatedAt
			writeEmojiStores(paths, all)
			return
		}
	}

	all = append(all, r)
	writeEmojiStores(paths, all)
}

func LoadEmojis(dataDir string) []EmojiRecord {
	emojiStoreMu.Lock()
	defer emojiStoreMu.Unlock()

	paths := emojiStorePaths(dataDir)
	seen := map[string]bool{}
	var all []EmojiRecord

	for _, path := range paths {
		var items []EmojiRecord
		if b, e := os.ReadFile(path); e == nil && json.Unmarshal(b, &items) == nil {
			for _, item := range items {
				key := item.ID
				if key == "" && item.CustomEmojiID != 0 {
					key = strconv.FormatInt(item.CustomEmojiID, 10)
				}
				if key != "" && !seen[key] {
					seen[key] = true
					all = append(all, item)
				}
			}
		}
	}
	return all
}

func FindEmoji(dataDir string, id string) *EmojiRecord {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	emojis := LoadEmojis(dataDir)
	for _, e := range emojis {
		if e.ID == id || (e.CustomEmojiID != 0 && strconv.FormatInt(e.CustomEmojiID, 10) == id) {
			return &e
		}
	}
	return nil
}

func emojiStorePaths(dataDir string) []string {
	paths := []string{filepath.Join(dataDir, "emoji.json")}
	if abs, err := filepath.Abs(dataDir); err == nil {
		root := filepath.Dir(abs)
		paths = append(paths, filepath.Join(root, "emoji.json"))
		paths = append(paths, filepath.Join(root, "data", "emoji.json"))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		norm := filepath.Clean(p)
		if !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	return out
}

func writeEmojiStores(paths []string, v []EmojiRecord) {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return
	}
	for _, path := range paths {
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		_ = os.WriteFile(path, b, 0644)
	}
}
