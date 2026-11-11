package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// non-thread-safe addons must be serialized
func TestNonThreadSafeAddonSerialized(t *testing.T) {
	a := &LoadedAddon{Name: "serialized"}
	const n = 16

	var active, maxActive int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			release := a.enterCall()
			cur := atomic.AddInt32(&active, 1)
			for {
				max := atomic.LoadInt32(&maxActive)
				if cur <= max || atomic.CompareAndSwapInt32(&maxActive, max, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&active, -1)
			release()
		}()
	}
	close(start)
	wg.Wait()

	if maxActive != 1 {
		t.Fatalf("max concurrent entries = %d, want 1", maxActive)
	}
}

// thread-safe addons run concurrently
func TestThreadSafeAddonRunsConcurrently(t *testing.T) {
	a := &LoadedAddon{Name: "parallel", flags: pluginFlagThreadSafe}
	const n = 8

	inside := make(chan struct{}, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel := a.enterCall()
			inside <- struct{}{}
			<-release
			rel()
		}()
	}

	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-inside:
		case <-deadline:
			close(release)
			wg.Wait()
			t.Fatalf("only %d/%d calls entered concurrently; THREAD_SAFE not honored", i, n)
		}
	}
	close(release)
	wg.Wait()
}

// reentrant call from same plugin thread must not deadlock
func TestNonThreadSafeReentrantCall(t *testing.T) {
	a := &LoadedAddon{Name: "reentrant"}

	done := make(chan struct{})
	go func() {
		releaseScope := beginPluginScope(a.Name, nil)
		defer releaseScope()
		if !inPluginScope(a.Name) {
			return
		}
		// second enterCall happens within same plugin scope
		releaseCall := a.enterCall()
		releaseCall()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reentrant enterCall deadlocked or scope not visible")
	}
}
