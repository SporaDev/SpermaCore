package media

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StickerRecord struct {
	ID            string    `json:"id"`
	ChatID        int64     `json:"chat_id"`
	MessageID     int       `json:"message_id"`
	DocumentID    int64     `json:"document_id,omitempty"`
	AccessHash    int64     `json:"access_hash,omitempty"`
	FileReference []byte    `json:"file_reference,omitempty"`
	FileID        int       `json:"file_id,omitempty"`
	Emoji         string    `json:"emoji,omitempty"`
	Description   string    `json:"description"`
	File          string    `json:"file,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

var stickerStoreMu sync.Mutex

func RememberSticker(dataDir string, r StickerRecord) {
	if strings.TrimSpace(r.Description) == "" {
		return
	}
	stickerStoreMu.Lock()
	defer stickerStoreMu.Unlock()
	paths := stickerStorePaths(dataDir)
	var all []StickerRecord
	seen := map[string]bool{}
	for _, path := range paths {
		var items []StickerRecord
		if b, e := os.ReadFile(path); e == nil && json.Unmarshal(b, &items) == nil {
			for _, item := range items {
				key := item.ID
				if key == "" {
					key = fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
				}
				if !seen[key] {
					all = append(all, item)
					seen[key] = true
				}
			}
		}
	}
	if strings.TrimSpace(r.ID) == "" {
		maxID := 0
		for _, item := range all {
			var n int
			if _, err := fmt.Sscanf(item.ID, "%d", &n); err == nil && n > maxID {
				maxID = n
			}
		}
		r.ID = strconv.Itoa(maxID + 1)
	}
	for i := range all {
		if all[i].ID == r.ID {
			all[i] = r
			writeStickerStores(paths, all)
			return
		}
	}
	all = append(all, r)
	writeStickerStores(paths, all)
}

func stickerStorePaths(dataDir string) []string {
	paths := []string{filepath.Join(dataDir, "stickers.json")}
	if abs, err := filepath.Abs(dataDir); err == nil {
		root := filepath.Dir(abs)
		paths = append(paths, filepath.Join(root, "stickers.json"))
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func writeStickerStores(paths []string, v []StickerRecord) {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return
	}
	for _, path := range paths {
		_ = os.MkdirAll(filepath.Dir(path), 0755)
		_ = os.WriteFile(path, b, 0644)
	}
}
