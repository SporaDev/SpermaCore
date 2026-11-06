package main

import (
	"context"
	"sync"
	"time"
)

// ChatGate coordinates concurrent access to a single chat with context cancellation,
// timeout support, in-flight task tracking, and non-blocking locks.
type ChatGate struct {
	sem    chan struct{}
	mu     sync.Mutex
	cancel context.CancelFunc
}

func newChatGate() *ChatGate {
	g := &ChatGate{
		sem: make(chan struct{}, 1),
	}
	g.sem <- struct{}{} // 1 permit available initially
	return g
}

// Acquire acquires the gate permit or returns ctx.Err() if the context expires first.
func (g *ChatGate) Acquire(ctx context.Context) error {
	select {
	case <-g.sem:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AcquireWithTimeout attempts to acquire the gate within a specific duration.
func (g *ChatGate) AcquireWithTimeout(d time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return g.Acquire(ctx)
}

// AcquireWithDone acquires the gate or aborts if the done channel is closed.
func (g *ChatGate) AcquireWithDone(done <-chan struct{}) bool {
	select {
	case <-g.sem:
		return true
	case <-done:
		return false
	}
}

// TryAcquire attempts to acquire the gate immediately without blocking.
func (g *ChatGate) TryAcquire() bool {
	select {
	case <-g.sem:
		return true
	default:
		return false
	}
}

// Release releases the acquired gate permit.
func (g *ChatGate) Release() {
	select {
	case g.sem <- struct{}{}:
	default:
		// already available (defensive)
	}
}

// Lock implements sync.Locker for backwards compatibility.
func (g *ChatGate) Lock() {
	<-g.sem
}

// Unlock implements sync.Locker for backwards compatibility.
func (g *ChatGate) Unlock() {
	g.Release()
}

// RegisterInFlight registers an active cancellation function for the current task.
func (g *ChatGate) RegisterInFlight(cancel context.CancelFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancel = cancel
}

// ClearInFlight removes the active cancellation function.
func (g *ChatGate) ClearInFlight() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancel = nil
}

// CancelInFlight cancels any currently active in-flight task for this chat.
func (g *ChatGate) CancelInFlight() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
		return true
	}
	return false
}

var (
	chatGatesMu sync.Mutex
	chatGates   = map[int64]*ChatGate{}
)

// getChatGate returns the per-chat gate instance.
func getChatGate(chatID int64) *ChatGate {
	lockChatsIdle.touch(chatID)
	chatGatesMu.Lock()
	defer chatGatesMu.Unlock()
	g, ok := chatGates[chatID]
	if !ok {
		g = newChatGate()
		chatGates[chatID] = g
	}
	return g
}

// getChatLock returns the per-chat gate as a sync.Locker (for compatibility).
func getChatLock(chatID int64) *ChatGate {
	return getChatGate(chatID)
}

// deleteChatGate removes chat gate on idle eviction.
func deleteChatGate(chatID int64) {
	chatGatesMu.Lock()
	delete(chatGates, chatID)
	chatGatesMu.Unlock()
	lockChatsIdle.forget(chatID)
}
