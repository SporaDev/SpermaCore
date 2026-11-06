package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	scEmbedsc "spermacore/core/embedsc"
)

// shutdown state and synchronization primitives
var (
	shutdownCh   = make(chan struct{})
	acceptMu     sync.Mutex
	acceptClosed bool

	tasksWG  sync.WaitGroup
	submitWG sync.WaitGroup
)

// submitTask enqueues a worker task
func submitTask(t workerTask) bool {
	acceptMu.Lock()
	if acceptClosed {
		acceptMu.Unlock()
		return false
	}
	submitWG.Add(1)
	tasksWG.Add(1)
	acceptMu.Unlock()

	defer submitWG.Done()

	select {
	case taskQueue <- t:
		return true
	case <-shutdownCh:
		tasksWG.Done()
		return false
	case <-time.After(30 * time.Second):
		tasksWG.Done()
		return false
	}
}

// beginShutdown initiates shutdown sequence
func beginShutdown() {
	acceptMu.Lock()
	already := acceptClosed
	acceptClosed = true
	acceptMu.Unlock()
	if already {
		return
	}

	close(shutdownCh)
	submitWG.Wait()

	for {
		select {
		case <-taskQueue:
			tasksWG.Done()
			metricQueueLen.Add(-1)
		default:
			return
		}
	}
}

func forceExit(log *Logger) {
	fmt.Print("\033[?25h\n")
	if log != nil {
		log.Error("force shutdown - exiting now")
	}
	fmt.Println("SpermaCore force-stopped.")
	os.Exit(130)
}

func closeAccepting() {
	acceptMu.Lock()
	acceptClosed = true
	acceptMu.Unlock()
	submitWG.Wait()
}

// gracefulShutdown performs clean application shutdown
func gracefulShutdown(log *Logger, addons []*LoadedAddon, embeddingStarted bool) {
	// flush widget state
	widgetRegistry.stateMu.Lock()
	widgetRegistry.saveStateLocked()
	widgetRegistry.stateMu.Unlock()
	log.Info("shutdown: widget state saved")

	// flush grouper buffers
	if g := GlobalGrouper(); g != nil {
		g.FlushAll()
		log.Info("shutdown: message buffers flushed")
	}

	closeAccepting()

	log.Info("shutdown: waiting for pending processing tasks...")
	tasksWG.Wait()
	log.Info("shutdown: processing tasks completed")

	select {
	case <-shutdownCh:
	default:
		close(shutdownCh)
	}

	// wait for background deliveries
	log.Info("shutdown: waiting for background deliveries...")
	bgSendWG.Wait()
	log.Info("shutdown: all messages delivered")

	if embeddingStarted {
		if err := scEmbedsc.Close(); err != nil {
			log.Warn("shutdown: embedsc close error: %v", err)
		}
	}

	for _, a := range addons {
		if rc := a.onShutdown(); rc != 0 && rc != -7 {
			log.Warn("module %s: on_shutdown error rc=%d", a.Name, rc)
		}
		a.deinit()
		a.Unload()
		log.Info("module %s unloaded", a.Name)
	}

	CloseAppDB()
	log.Info("shutdown: database closed")

	if gDash != nil {
		gDash.Stop()
	}
}
