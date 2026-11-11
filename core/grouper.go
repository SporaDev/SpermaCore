package main

import (
	"encoding/json"
	scgrouper "spermacore/core/grouper"
	"sync"
	"time"
)

type GrouperConfig struct {
	Enabled         bool
	GroupsOnly      bool
	Window, MaxWait time.Duration
	Joiner          string
}

var grouperDefaults = map[string]any{"enabled": true, "groups_only": false, "grouper_time": "1.5s", "window_ms": 1500, "max_wait_ms": 10000, "joiner": "\n"}

func LoadGrouperConfig(s map[string]any) *GrouperConfig {
	c := &GrouperConfig{Enabled: true, Window: 3 * time.Second, MaxWait: 10 * time.Second, Joiner: "\n"}
	if s != nil {
		if v, ok := getBool(s, "enabled"); ok {
			c.Enabled = v
		}
		if v, ok := getBool(s, "groups_only"); ok {
			c.GroupsOnly = v
		}
		// grouper_time takes precedence over window_ms
		if v, ok := getString(s, "grouper_time"); ok {
			if d, err := time.ParseDuration(v); err == nil && d >= 0 {
				c.Window = d
			}
		} else if v, ok := getInt(s, "window_ms"); ok && v >= 0 {
			c.Window = time.Duration(v) * time.Millisecond
		}
		if v, ok := getInt(s, "max_wait_ms"); ok && v > 0 {
			c.MaxWait = time.Duration(v) * time.Millisecond
		}
		if v, ok := getString(s, "joiner"); ok && v != "" {
			c.Joiner = v
		}
	}
	if c.MaxWait < c.Window {
		c.MaxWait = c.Window
	}
	return c
}

type Grouper struct{ inner *scgrouper.Grouper }

func pollToJSON(p *Poll) string {
	if p == nil {
		return ""
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}

func NewGrouper(c *GrouperConfig, flush func(transportEvent)) *Grouper {
	if c == nil {
		c = LoadGrouperConfig(nil)
	}
	return &Grouper{inner: scgrouper.New(scgrouper.Config{Enabled: c.Enabled, GroupsOnly: c.GroupsOnly, Window: c.Window, MaxWait: c.MaxWait, Joiner: c.Joiner}, func(e scgrouper.Event) {
		ev := transportEvent{Type: e.Type, Transport: e.Transport, ChatID: e.ChatID, SenderID: e.SenderID, SenderName: e.SenderName, SenderUsername: e.SenderUsername, MessageID: e.MessageID, ReplyToID: e.ReplyToID, TopicID: e.TopicID, ReplyFromSelf: e.ReplyFromSelf, SelfUsername: e.SelfUsername, SelfName: e.SelfName, SelfID: e.SelfID, Forum: e.Forum, Text: e.Text, MediaType: e.MediaType, PhotoPath: e.PhotoPath, Date: e.Date, CustomEmojiIDs: e.CustomEmojiIDs, StickerID: e.StickerID, StickerEmoji: e.StickerEmoji, StickerAccessHash: e.StickerAccessHash, StickerFileRef: e.StickerFileRef, StickerFileID: e.StickerFileID, StickerAnimated: e.StickerAnimated, StickerThumbPath: e.StickerThumbPath, StickerWidth: e.StickerWidth, StickerHeight: e.StickerHeight, SemanticContext: e.SemanticContext, Activation: e.Activation, InlineContext: e.InlineContext}
		if e.PollJSON != "" {
			var p Poll
			if err := json.Unmarshal([]byte(e.PollJSON), &p); err == nil {
				ev.Poll = &p
			}
		}
		flush(ev)
	})}
}
func (g *Grouper) Enabled() bool { return g != nil && g.inner.Enabled() }
func (g *Grouper) Add(e transportEvent) {
	g.inner.Add(scgrouper.Event{Type: e.Type, Transport: e.Transport, ChatID: e.ChatID, SenderID: e.SenderID, SenderName: e.SenderName, SenderUsername: e.SenderUsername, MessageID: e.MessageID, ReplyToID: e.ReplyToID, TopicID: e.TopicID, ReplyFromSelf: e.ReplyFromSelf, SelfUsername: e.SelfUsername, SelfName: e.SelfName, SelfID: e.SelfID, Forum: e.Forum, Text: e.Text, MediaType: e.MediaType, PhotoPath: e.PhotoPath, Date: e.Date, CustomEmojiIDs: e.CustomEmojiIDs, StickerID: e.StickerID, StickerEmoji: e.StickerEmoji, StickerAccessHash: e.StickerAccessHash, StickerFileRef: e.StickerFileRef, StickerFileID: e.StickerFileID, StickerAnimated: e.StickerAnimated, StickerThumbPath: e.StickerThumbPath, StickerWidth: e.StickerWidth, StickerHeight: e.StickerHeight, SemanticContext: e.SemanticContext, Activation: e.Activation, InlineContext: e.InlineContext, PollJSON: pollToJSON(e.Poll)})
}
func (g *Grouper) FlushAll() {
	if g != nil {
		g.inner.FlushAll()
	}
}

var (
	grouperMu   sync.RWMutex
	grouperInst *Grouper
)

func InitGrouper(c *GrouperConfig, f func(transportEvent)) {
	grouperMu.Lock()
	defer grouperMu.Unlock()
	grouperInst = NewGrouper(c, f)
}
func GlobalGrouper() *Grouper { grouperMu.RLock(); defer grouperMu.RUnlock(); return grouperInst }

// update grouper config on the fly
func UpdateGrouperConfig(c *GrouperConfig) {
	g := GlobalGrouper()
	if g == nil || c == nil {
		return
	}
	g.inner.SetConfig(scgrouper.Config{
		Enabled:    c.Enabled,
		GroupsOnly: c.GroupsOnly,
		Window:     c.Window,
		MaxWait:    c.MaxWait,
		Joiner:     c.Joiner,
	})
}
func getBool(m map[string]any, k string) (bool, bool) { v, ok := m[k].(bool); return v, ok }
func getInt(m map[string]any, k string) (int64, bool) {
	switch v := m[k].(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}
func getString(m map[string]any, k string) (string, bool) { v, ok := m[k].(string); return v, ok }
