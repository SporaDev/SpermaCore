package main

import (
	"sync"
)

// max messages kept in memory per chat
const maxHistoryPerChat = 20

// chatHistory stores in-memory conversation history per chat
type ChatHistory struct {
	// rwmutex protecting byChat map
	mu     sync.RWMutex
	byChat map[int64][]ChatMessage
}

func NewChatHistory() *ChatHistory {
	return &ChatHistory{byChat: make(map[int64][]ChatMessage)}
}

// add appends message to chat history, trims oldest entries and persists to SQLite
func (h *ChatHistory) Add(chatID int64, msg ChatMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	historyChatsIdle.touch(chatID)

	cur := h.byChat[chatID]

	// append new message
	cur = append(cur, msg)

	if len(cur) > maxHistoryPerChat {
		// copy tail slice to release backing array
		tail := make([]ChatMessage, maxHistoryPerChat)
		copy(tail, cur[len(cur)-maxHistoryPerChat:])
		cur = tail
	}

	h.byChat[chatID] = cur
	dbSaveChatMessage(chatID, msg.Role, msg.Content)
}

// get returns shallow copy of chat history, restoring from SQLite if cold
func (h *ChatHistory) Get(chatID int64) []ChatMessage {
	h.mu.RLock()
	orig := h.byChat[chatID]
	h.mu.RUnlock()

	if len(orig) == 0 {
		dbMsgs := dbLoadChatHistory(chatID, maxHistoryPerChat)
		if len(dbMsgs) > 0 {
			h.mu.Lock()
			if len(h.byChat[chatID]) == 0 {
				h.byChat[chatID] = dbMsgs
			}
			orig = h.byChat[chatID]
			h.mu.Unlock()
		}
	}

	if len(orig) == 0 {
		return nil
	}

	out := make([]ChatMessage, len(orig))
	copy(out, orig)
	return out
}

// clear removes history for specified chat from memory and SQLite
func (h *ChatHistory) Clear(chatID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byChat, chatID)
	historyChatsIdle.forget(chatID)
	dbClearChatHistory(chatID)
}
