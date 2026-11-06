// plugin webui widget registry (ABI 1.6.1)
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"spermacore/core/webui"
)

// widgetEntry tracks a single registered widget.
type widgetEntry struct {
	Info *WidgetInfo

	// lastData caches most recent data for late hello snapshots.
	lastData string
}

// widgetRegistryState stores global widget registry.
type widgetRegistryState struct {
	mu      sync.RWMutex
	widgets map[string]*widgetEntry // id -> widget

	statePath string
	stateMu   sync.Mutex
	stateData map[string]string // id -> saved state json

	started bool
}

var widgetRegistry = &widgetRegistryState{
	widgets:   map[string]*widgetEntry{},
	stateData: map[string]string{},
}

// registerWidgetsFromAddons gathers widgets from loaded addons.
func registerWidgetsFromAddons(addons []*LoadedAddon, cfg *Config, log *Logger) {
	widgetRegistry.mu.Lock()
	defer widgetRegistry.mu.Unlock()

	widgetRegistry.statePath = filepath.Join(cfg.Core.DataDir, "widgets.state.json")
	widgetRegistry.loadStateLocked()

	total := 0
	for _, a := range addons {
		if a == nil {
			continue
		}
		list := a.Widgets()
		for _, w := range list {
			if w.ID == "" {
				continue
			}
			if _, exists := widgetRegistry.widgets[w.ID]; exists {
				log.Warn("widget id collision: %s (skipping from %s)", w.ID, a.Name)
				continue
			}
			widgetRegistry.widgets[w.ID] = &widgetEntry{Info: w}
			total++
			log.Info("widget registered: %s (%s) from %s", w.ID, w.Title, a.Name)
		}
	}
	log.Info("widget registry: %d widget(s) total", total)
}

// SnapshotWidgets builds widget list for webui hello handshake.
func (r *widgetRegistryState) SnapshotWidgets() []webui.WidgetPayload {
	// snapshot entries under lock, call render without lock to avoid deadlocks
	r.mu.RLock()
	type snap struct {
		entry *widgetEntry
		data  string
	}
	snaps := make([]snap, 0, len(r.widgets))
	for _, e := range r.widgets {
		snaps = append(snaps, snap{entry: e, data: e.lastData})
	}
	r.mu.RUnlock()

	out := make([]webui.WidgetPayload, 0, len(snaps))
	for _, s := range snaps {
		e := s.entry
		p := webui.WidgetPayload{
			ID:    e.Info.ID,
			Title: e.Info.Title,
			Icon:  e.Info.Icon,
			W:     e.Info.DefaultW,
			H:     e.Info.DefaultH,
		}
		if p.W <= 0 {
			p.W = 300
		}
		if p.H <= 0 {
			p.H = 200
		}
		// initial html from render
		if html := e.Info.Render(); html != "" {
			p.HTML = html
		}
		// current data
		if s.data != "" {
			p.Data = s.data
		}
		out = append(out, p)
	}
	return out
}

// emit pushes widget data.
func (r *widgetRegistryState) emit(id, dataJSON string) int {
	r.mu.Lock()
	e, ok := r.widgets[id]
	if ok {
		e.lastData = dataJSON
	}
	r.mu.Unlock()
	if !ok {
		return -3 // SC_ERR_NOT_FOUND
	}
	if w := webui.Global(); w != nil {
		w.WidgetData(id, dataJSON)
	}
	return 0
}

// notify pushes a webui toast.
func (r *widgetRegistryState) notify(id string, level int, msg string, durationMs uint32) int {
	if w := webui.Global(); w != nil {
		w.WidgetNotify(id, level, msg, durationMs)
	}
	return 0
}

// state persistence
func (r *widgetRegistryState) stateSet(id, stateJSON string) int {
	r.stateMu.Lock()
	r.stateData[id] = stateJSON
	r.saveStateLocked()
	r.stateMu.Unlock()
	return 0
}

func (r *widgetRegistryState) stateGet(id string) string {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.stateData[id]
}

// startUpdater runs periodic 2s poll for widget updates.
func (r *widgetRegistryState) startUpdater() {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.mu.Unlock()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			r.tick()
		}
	}()
}

func (r *widgetRegistryState) tick() {
	r.mu.RLock()
	ids := make([]string, 0, len(r.widgets))
	entries := make([]*widgetEntry, 0, len(r.widgets))
	for id, e := range r.widgets {
		ids = append(ids, id)
		entries = append(entries, e)
	}
	r.mu.RUnlock()

	w := webui.Global()
	if w == nil {
		return
	}

	for i, e := range entries {
		data := e.Info.Update()
		if data == "" {
			continue
		}
		r.mu.Lock()
		e.lastData = data
		r.mu.Unlock()
		w.WidgetData(ids[i], data)
	}
}

// handleClick invokes widget click handler and returns ui command json.
func (r *widgetRegistryState) handleClick(id, payloadJSON string) string {
	r.mu.RLock()
	e, ok := r.widgets[id]
	r.mu.RUnlock()
	if !ok {
		return ""
	}
	return e.Info.Click(payloadJSON)
}

// state persistence: data/widgets.state.json
func (r *widgetRegistryState) loadStateLocked() {
	if r.statePath == "" {
		return
	}
	raw, err := os.ReadFile(r.statePath)
	if err != nil {
		return
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) == nil {
		r.stateData = m
	}
}

func (r *widgetRegistryState) saveStateLocked() {
	if r.statePath == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(r.statePath), 0o755)
	raw, err := json.MarshalIndent(r.stateData, "", "  ")
	if err != nil {
		return
	}
	tmp := r.statePath + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, r.statePath)
	}
}

// cgo exports
func widgetEmit(id, dataJSON string) int {
	return widgetRegistry.emit(id, dataJSON)
}

func widgetNotify(id string, level int, msg string, durationMs uint32) int {
	return widgetRegistry.notify(id, level, msg, durationMs)
}

func widgetStateSet(id, stateJSON string) int {
	return widgetRegistry.stateSet(id, stateJSON)
}

func widgetStateGet(id string) string {
	return widgetRegistry.stateGet(id)
}
