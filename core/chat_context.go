package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

var contextDefaults = map[string]any{
	"enabled":         true,
	"recent_messages": 12,
	"max_chars":       240,
	"max_age_minutes": 360,
}

type chatContextConfig struct {
	Enabled     bool
	MaxMessages int
	MaxChars    int
	MaxAge      time.Duration
}

type recentLine struct {
	MessageID      int
	SenderName     string
	SenderUsername string
	Text           string
	Self           bool
	At             time.Time
}

var (
	chatContextMu   sync.Mutex
	chatContextCfg  = chatContextConfig{Enabled: true, MaxMessages: 12, MaxChars: 240, MaxAge: 6 * time.Hour}
	chatContextData = map[int64][]recentLine{}
)

func loadChatContextConfig(section map[string]any) {
	c := chatContextConfig{Enabled: true, MaxMessages: 12, MaxChars: 240, MaxAge: 6 * time.Hour}
	if section != nil {
		if v, ok := getBool(section, "enabled"); ok {
			c.Enabled = v
		}
		if v, ok := getInt(section, "recent_messages"); ok && v > 0 {
			c.MaxMessages = int(v)
		}
		if v, ok := getInt(section, "max_chars"); ok && v > 0 {
			c.MaxChars = int(v)
		}
		if v, ok := getInt(section, "max_age_minutes"); ok && v > 0 {
			c.MaxAge = time.Duration(v) * time.Minute
		}
	}
	chatContextMu.Lock()
	chatContextCfg = c
	chatContextMu.Unlock()
}

func rememberIncomingMessage(ev transportEvent) {
	text := strings.TrimSpace(ev.Text)
	if text == "" && ev.MediaType != "" {
		text = "[" + ev.MediaType + "]"
	}
	appendRecentLine(ev.ChatID, recentLine{
		MessageID:      ev.MessageID,
		SenderName:     ev.SenderName,
		SenderUsername: ev.SenderUsername,
		Text:           text,
		At:             time.Now(),
	})
}

func rememberOutgoingMessage(ev transportEvent, text string) {
	appendRecentLine(ev.ChatID, recentLine{
		MessageID: 0,
		Text:      strings.TrimSpace(text),
		Self:      true,
		At:        time.Now(),
	})
}

func appendRecentLine(chatID int64, line recentLine) {
	if line.Text == "" {
		return
	}
	chatContextMu.Lock()
	defer chatContextMu.Unlock()
	if !chatContextCfg.Enabled {
		return
	}
	lines := chatContextData[chatID]
	if line.MessageID > 0 && len(lines) > 0 {
		for i, old := range lines {
			if old.MessageID == line.MessageID {
				if old.Text != line.Text {
					lines[i].Text = line.Text
					lines[i].At = time.Now()
				}
				return
			}
		}
	}
	kept := make([]recentLine, 0, len(lines)+1)
	if chatContextCfg.MaxAge > 0 {
		cutoff := time.Now().Add(-chatContextCfg.MaxAge)
		for _, old := range lines {
			if old.At.After(cutoff) {
				kept = append(kept, old)
			}
		}
	} else {
		kept = append(kept, lines...)
	}
	kept = append(kept, line)
	hardCap := chatContextCfg.MaxMessages * 4
	if hardCap < 32 {
		hardCap = 32
	}
	if len(kept) > hardCap {
		kept = kept[len(kept)-hardCap:]
	}
	chatContextData[chatID] = kept
	contextChatsIdle.touch(chatID)
}

// build recent transcript block for prompt
func recentChatTranscript(chatID int64, excludeMessageID int) string {
	chatContextMu.Lock()
	cfg := chatContextCfg
	lines := append([]recentLine(nil), chatContextData[chatID]...)
	chatContextMu.Unlock()
	if !cfg.Enabled || len(lines) == 0 {
		return ""
	}

	// exclude current message and stale lines
	now := time.Now()
	filtered := make([]recentLine, 0, len(lines))
	for _, line := range lines {
		if excludeMessageID > 0 && line.MessageID == excludeMessageID {
			continue
		}
		if cfg.MaxAge > 0 && !line.At.IsZero() && now.Sub(line.At) > cfg.MaxAge {
			continue
		}
		filtered = append(filtered, line)
	}
	if len(filtered) > cfg.MaxMessages {
		filtered = filtered[len(filtered)-cfg.MaxMessages:]
	}

	var b strings.Builder
	for _, line := range filtered {
		who := "you"
		if !line.Self {
			who = strings.TrimSpace(line.SenderName)
			if line.SenderUsername != "" {
				if who != "" {
					who += " "
				}
				who += "(@" + strings.TrimPrefix(line.SenderUsername, "@") + ")"
			}
			if who == "" {
				who = "someone"
			}
		}
		text := strings.ReplaceAll(line.Text, "\n", " ")
		text = truncate(text, cfg.MaxChars)
		fmt.Fprintf(&b, "[%s] %s: %s\n", line.At.Local().Format("15:04"), who, text)
	}
	return strings.TrimSpace(b.String())
}
