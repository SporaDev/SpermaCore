package main

import (
	"sort"
	"sync"
	"time"

	"spermacore/core/dashboard"
	scEmbedsc "spermacore/core/embedsc"
)

// per-chat state tracking and background cleanup janitor
const (
	chatStateTTL      = 7 * 24 * time.Hour
	chatStateMaxChats = 2000
	chatJanitorEvery  = 5 * time.Minute
)

// idleMap tracks chat_id -> lastUsed
type idleMap struct {
	mu   sync.Mutex
	last map[int64]time.Time
	max  int
}

func newIdleMap(max int) *idleMap {
	return &idleMap{last: make(map[int64]time.Time), max: max}
}

func (m *idleMap) touch(id int64) {
	m.mu.Lock()
	m.last[id] = time.Now()
	m.mu.Unlock()
}

func (m *idleMap) forget(id int64) {
	m.mu.Lock()
	delete(m.last, id)
	m.mu.Unlock()
}

func (m *idleMap) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.last)
}

// prune removes entries by ttl and lru limit
func (m *idleMap) prune(now time.Time, ttl time.Duration) []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()

	var drop []int64
	for id, at := range m.last {
		if ttl > 0 && now.Sub(at) > ttl {
			drop = append(drop, id)
			delete(m.last, id)
		}
	}

	if m.max > 0 && len(m.last) > m.max {
		type entry struct {
			id int64
			at time.Time
		}
		rest := make([]entry, 0, len(m.last))
		for id, at := range m.last {
			rest = append(rest, entry{id, at})
		}
		sort.Slice(rest, func(i, j int) bool { return rest[i].at.Before(rest[j].at) })
		for i := 0; i < len(rest)-m.max; i++ {
			drop = append(drop, rest[i].id)
			delete(m.last, rest[i].id)
		}
	}
	return drop
}

// per-structure idle registries
var (
	historyChatsIdle = newIdleMap(chatStateMaxChats)
	contextChatsIdle = newIdleMap(chatStateMaxChats)
	lockChatsIdle    = newIdleMap(0)
	groupChatsIdle   = newIdleMap(chatStateMaxChats)
)

// deleteChatContext removes chat transcript
func deleteChatContext(chatID int64) {
	chatContextMu.Lock()
	delete(chatContextData, chatID)
	chatContextMu.Unlock()
	contextChatsIdle.forget(chatID)
}

// deleteChatLock removes chat lock/gate
func deleteChatLock(chatID int64) {
	deleteChatGate(chatID)
}

// forgetGroupChat removes group activity state
func forgetGroupChat(chatID int64) {
	groupRuntimeMu.Lock()
	delete(groupChats, chatID)
	groupRuntimeMu.Unlock()
	groupChatsIdle.forget(chatID)
}

// pruneChatState runs cleanup across per-chat structures
func pruneChatState(now time.Time) (dashboard.ChatCounts, int) {
	removed := 0

	drop := historyChatsIdle.prune(now, chatStateTTL)
	for _, id := range drop {
		gHistory.Clear(id)
	}
	removed += len(drop)

	drop = contextChatsIdle.prune(now, chatStateTTL)
	for _, id := range drop {
		deleteChatContext(id)
	}
	removed += len(drop)

	drop = lockChatsIdle.prune(now, chatStateTTL)
	for _, id := range drop {
		deleteChatLock(id)
	}
	removed += len(drop)

	drop = groupChatsIdle.prune(now, chatStateTTL)
	for _, id := range drop {
		forgetGroupChat(id)
	}
	removed += len(drop)

	removed += scEmbedsc.PruneStores(now.Add(-chatStateTTL), chatStateMaxChats)
	removed += telegramPruneOwnMessages(now.Add(-chatStateTTL).Unix())

	return chatStateSnapshot(), removed
}

// chatStateSnapshot returns counts for dashboard
func chatStateSnapshot() dashboard.ChatCounts {
	ownMessages, _ := telegramOwnMessageStats()
	return dashboard.ChatCounts{
		History:     historyChatsIdle.len(),
		Context:     contextChatsIdle.len(),
		Locks:       lockChatsIdle.len(),
		Groups:      groupChatsIdle.len(),
		Embed:       scEmbedsc.StoreCount(),
		OwnMessages: ownMessages,
	}
}

// startChatStateJanitor launches periodic cleanup
func startChatStateJanitor(log *Logger) {
	go func() {
		ticker := time.NewTicker(chatJanitorEvery)
		defer ticker.Stop()
		for {
			select {
			case <-shutdownCh:
				return
			case now := <-ticker.C:
				counts, removed := pruneChatState(now)
				if removed > 0 {
					log.Info("chat-state janitor: removed %d idle entries (history=%d context=%d locks=%d groups=%d embed=%d own=%d)",
						removed, counts.History, counts.Context, counts.Locks, counts.Groups, counts.Embed, counts.OwnMessages)
				}
				if gDash != nil {
					gDash.SetChatCounts(counts)
				}
			}
		}
	}()
}
