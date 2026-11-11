package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// fakeHumanizer provides deterministic delays for tests.
type fakeHumanizer struct {
	delay time.Duration
}

func (f fakeHumanizer) Humanize(s string) string         { return s }
func (f fakeHumanizer) DelayBefore(string) time.Duration { return f.delay }
func (f fakeHumanizer) DelayBetween() time.Duration      { return f.delay }

// sleepOrShutdown interrupts immediately on shutdown.
func TestSleepOrShutdownInterrupts(t *testing.T) {
	closed := make(chan struct{})
	close(closed)

	start := time.Now()
	if ok := sleepOrShutdown(10*time.Second, closed); ok {
		t.Fatal("sleepOrShutdown returned true on closed channel")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel took %v, expected immediately", elapsed)
	}
}

func TestSleepOrShutdownWaitsFullDelay(t *testing.T) {
	open := make(chan struct{})
	start := time.Now()
	if ok := sleepOrShutdown(50*time.Millisecond, open); !ok {
		t.Fatal("sleepOrShutdown cancelled without shutdown")
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("pause too short: %v", elapsed)
	}
}

// sendHumanizedParts runs tasks concurrently while preserving order.
func TestSendHumanizedPartsConcurrentPreservesOrder(t *testing.T) {
	const (
		tasks     = 20
		parts     = 3
		partDelay = 200 * time.Millisecond
	)

	h := fakeHumanizer{delay: partDelay}
	done := make(chan struct{})

	var mu sync.Mutex
	got := make(map[int][]string, tasks)

	var wg sync.WaitGroup
	start := time.Now()
	for task := 0; task < tasks; task++ {
		wg.Add(1)
		go func(task int) {
			defer wg.Done()
			texts := make([]string, parts)
			for i := range texts {
				texts[i] = fmt.Sprintf("t%02d-p%d", task, i)
			}
			ok := sendHumanizedParts(texts, h, done, func(_ int, text string) error {
				mu.Lock()
				got[task] = append(got[task], text)
				mu.Unlock()
				return nil
			})
			if !ok {
				t.Errorf("task %d: send aborted without shutdown", task)
			}
		}(task)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for task := 0; task < tasks; task++ {
		want := make([]string, parts)
		for i := range want {
			want[i] = fmt.Sprintf("t%02d-p%d", task, i)
		}
		if fmt.Sprint(got[task]) != fmt.Sprint(want) {
			t.Fatalf("task %d: part order broken: got %v want %v", task, got[task], want)
		}
	}

	// parallel run should finish in parts*delay order rather than tasks*parts*delay
	if elapsed > 5*time.Second {
		t.Fatalf("send took %v - tasks seem not parallelized", elapsed)
	}
}

// closed shutdown channel aborts sending immediately
func TestSendHumanizedPartsStopsOnShutdown(t *testing.T) {
	done := make(chan struct{})
	h := fakeHumanizer{delay: 5 * time.Second}

	sent := 0
	start := time.Now()
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(done)
	}()

	ok := sendHumanizedParts([]string{"a", "b", "c"}, h, done, func(int, string) error {
		sent++
		return nil
	})
	if ok {
		t.Fatal("sendHumanizedParts returned true on shutdown")
	}
	if sent != 0 {
		t.Fatalf("sent %d parts after shutdown", sent)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("shutdown took %v", elapsed)
	}
}
