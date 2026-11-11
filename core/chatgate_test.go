package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestChatGateAcquireRelease(t *testing.T) {
	gate := newChatGate()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := gate.Acquire(ctx); err != nil {
		t.Fatalf("failed to acquire gate: %v", err)
	}

	// Should not be available
	if gate.TryAcquire() {
		t.Fatal("expected TryAcquire to fail while held")
	}

	gate.Release()

	// Should be available now
	if !gate.TryAcquire() {
		t.Fatal("expected TryAcquire to succeed after release")
	}
	gate.Release()
}

func TestChatGateContextCancellation(t *testing.T) {
	gate := newChatGate()

	// Acquire first permit
	ctx := context.Background()
	if err := gate.Acquire(ctx); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Second acquire with short timeout must fail without blocking forever
	shortCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := gate.Acquire(shortCtx)
	if err == nil {
		t.Fatal("expected error on second acquire with expired context")
	}
	if err != context.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got: %v", err)
	}

	gate.Release()
}

func TestChatGateInFlightCancellation(t *testing.T) {
	gate := newChatGate()

	canceled := false
	cancelFn := func() {
		canceled = true
	}

	gate.RegisterInFlight(cancelFn)
	if !gate.CancelInFlight() {
		t.Fatal("expected CancelInFlight to return true")
	}
	if !canceled {
		t.Fatal("expected cancelFn to be executed")
	}

	// Subsequent cancel should return false
	if gate.CancelInFlight() {
		t.Fatal("expected second CancelInFlight to return false")
	}

	// Test ClearInFlight
	gate.RegisterInFlight(cancelFn)
	gate.ClearInFlight()
	if gate.CancelInFlight() {
		t.Fatal("expected CancelInFlight to be false after clear")
	}
}

func TestChatGateAcquireWithDone(t *testing.T) {
	gate := newChatGate()

	done := make(chan struct{})
	close(done)

	// Acquire permit first
	gate.Lock()

	// Try acquiring with closed done channel
	if gate.AcquireWithDone(done) {
		t.Fatal("expected AcquireWithDone to return false on closed channel")
	}

	gate.Unlock()
}

func TestChatGateConcurrentStress(t *testing.T) {
	const iters = 200
	const goroutines = 8

	gate := newChatGate()
	var counter int
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				if err := gate.Acquire(ctx); err != nil {
					cancel()
					t.Errorf("acquire failed: %v", err)
					return
				}
				counter++
				time.Sleep(10 * time.Microsecond)
				gate.Release()
				cancel()
			}
		}()
	}

	wg.Wait()
	if counter != iters*goroutines {
		t.Fatalf("expected counter %d, got %d", iters*goroutines, counter)
	}
}
