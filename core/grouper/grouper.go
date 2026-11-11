package grouper

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Enabled         bool
	GroupsOnly      bool
	Window, MaxWait time.Duration
	Joiner          string
}
type Event struct {
	Type, Transport, SenderName, SenderUsername string
	ChatID, SenderID                            int64
	MessageID, ReplyToID, TopicID               int
	ReplyFromSelf, Forum                        bool
	SelfUsername                                string
	SelfName                                    string
	SelfID                                      int64
	Text, MediaType, PhotoPath                  string
	Date                                        int64
	CustomEmojiIDs                              []int64
	StickerID, StickerEmoji                     string
	StickerAccessHash                           int64
	StickerFileRef                              []byte
	StickerFileID                               int
	StickerAnimated                             bool
	StickerThumbPath                            string
	StickerWidth                                int
	StickerHeight                               int
	SemanticContext                             string
	Activation                                  string
	InlineContext                               string
	// PollJSON carries the poll/quiz attached to the last message of the group
	// (JSON encoded so the grouper package stays independent of core types).
	PollJSON string
}
type buffer struct {
	texts            []string
	semanticContext  []string
	customEmojiIDs   []int64
	last             Event
	first            time.Time
	timer            *time.Timer
	hasReplyFromSelf bool
	replyToID        int
}
type bufferKey struct {
	chatID  int64
	topicID int
}

type Grouper struct {
	cfg     atomic.Pointer[Config]
	mu      sync.Mutex
	buffers map[bufferKey]*buffer
	flush   func(Event)
}

func New(c Config, f func(Event)) *Grouper {
	if c.Joiner == "" {
		c.Joiner = "\n"
	}
	g := &Grouper{buffers: map[bufferKey]*buffer{}, flush: f}
	g.cfg.Store(&c)
	return g
}

// set config dynamically
func (g *Grouper) SetConfig(c Config) {
	if g == nil {
		return
	}
	if c.Joiner == "" {
		c.Joiner = "\n"
	}
	g.cfg.Store(&c)
}

func (g *Grouper) conf() Config { return *g.cfg.Load() }

func (g *Grouper) Enabled() bool {
	return g != nil && g.cfg.Load().Enabled
}
func eventLine(e Event) string {
	text := strings.TrimSpace(e.Text)
	if text == "" {
		return text
	}
	// Private chats are a single conversation; the speaker is obvious, so do
	// not decorate the text with a sender prefix. Group chats (negative ids)
	// keep the prefix to distinguish participants.
	if e.ChatID > 0 {
		return text
	}
	who := strings.TrimSpace(e.SenderName)
	if e.SenderUsername != "" {
		if who != "" {
			who += " "
		}
		who += "(@" + strings.TrimPrefix(e.SenderUsername, "@") + ")"
	}
	if who == "" {
		who = fmt.Sprintf("user:%d", e.SenderID)
	}
	return "[" + who + "] " + text
}

func (g *Grouper) Add(e Event) {
	c := g.conf()
	if !c.Enabled || (c.GroupsOnly && e.ChatID >= 0) {
		g.flush(e)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := bufferKey{chatID: e.ChatID, topicID: e.TopicID}
	b, ok := g.buffers[key]
	now := time.Now()
	if !ok {
		b = &buffer{last: e, first: now, hasReplyFromSelf: e.ReplyFromSelf, replyToID: e.ReplyToID, customEmojiIDs: append([]int64(nil), e.CustomEmojiIDs...)}
		if strings.TrimSpace(e.Text) != "" {
			b.texts = []string{eventLine(e)}
		}
		if strings.TrimSpace(e.SemanticContext) != "" {
			b.semanticContext = []string{e.SemanticContext}
		}
		g.buffers[key] = b
		g.arm(key, b, c.Window)
		return
	}
	if len(e.CustomEmojiIDs) > 0 {
		b.customEmojiIDs = append(b.customEmojiIDs, e.CustomEmojiIDs...)
	}
	if strings.TrimSpace(e.Text) != "" {
		b.texts = append(b.texts, eventLine(e))
	}
	if strings.TrimSpace(e.SemanticContext) != "" {
		b.semanticContext = append(b.semanticContext, e.SemanticContext)
	}
	if e.ReplyFromSelf {
		b.hasReplyFromSelf = true
	}
	if e.ReplyToID > 0 {
		b.replyToID = e.ReplyToID
	}
	// prioritize direct mention in batch
	activation := e.Activation
	if b.last.Activation == "direct" || b.hasReplyFromSelf || e.Activation == "direct" {
		activation = "direct"
	}
	b.last = e
	b.last.Activation = activation
	b.last.ReplyFromSelf = b.hasReplyFromSelf
	if b.replyToID > 0 {
		b.last.ReplyToID = b.replyToID
	}
	if now.Sub(b.first) >= c.MaxWait {
		if b.timer != nil {
			b.timer.Stop()
		}
		go g.flushChat(key)
		return
	}
	if b.timer != nil {
		b.timer.Reset(c.Window)
	}
}
func (g *Grouper) arm(key bufferKey, b *buffer, window time.Duration) {
	b.timer = time.AfterFunc(window, func() { g.flushChat(key) })
}
func (g *Grouper) flushChat(key bufferKey) {
	g.mu.Lock()
	b, ok := g.buffers[key]
	if ok {
		delete(g.buffers, key)
	}
	g.mu.Unlock()
	if !ok {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.last.Text = strings.Join(b.texts, g.conf().Joiner)
	b.last.SemanticContext = strings.Join(b.semanticContext, "\n")
	if len(b.customEmojiIDs) > 0 {
		b.last.CustomEmojiIDs = b.customEmojiIDs
	}
	if len(b.texts) > 1 {
		if b.last.ChatID < 0 {
			b.last.SenderName = "group participants"
			b.last.SenderUsername = ""
		}
		if !b.hasReplyFromSelf {
			b.last.ReplyToID = 0
		}
	}
	g.flush(b.last)
}
func (g *Grouper) FlushAll() {
	if !g.Enabled() {
		return
	}
	g.mu.Lock()
	ids := make([]bufferKey, 0, len(g.buffers))
	for id := range g.buffers {
		ids = append(ids, id)
	}
	g.mu.Unlock()
	for _, id := range ids {
		g.flushChat(id)
	}
}
