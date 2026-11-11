package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentLoopAddonAndWidget(t *testing.T) {
	// 1. load agentloop addon
	testCfg := &Config{
		PluginSections: map[string]map[string]any{
			"agentloop": {
				"enabled":            true,
				"interval_min":       15,
				"active_hours_start": 9,
				"active_hours_end":   23,
				"max_per_day":        10,
			},
		},
	}
	moduleDir := filepath.Join("..", "modules", "agentloop")
	addon, err := loadOneAddon("agentloop", moduleDir, "..", testCfg, GetLogger("test.agentloop"))
	if err != nil {
		t.Fatalf("failed to load agentloop addon: %v", err)
	}
	defer addon.Unload()

	// 2. verify widgets
	widgets := addon.Widgets()
	if len(widgets) == 0 {
		t.Fatal("expected agentloop to register at least 1 widget")
	}

	var cycleWidget *WidgetInfo
	for _, w := range widgets {
		if w.ID == "agentloop.cycle" {
			cycleWidget = w
			break
		}
	}
	if cycleWidget == nil {
		t.Fatalf("agentloop.cycle widget not found among registered widgets: %+v", widgets)
	}

	if cycleWidget.Title != "Agent Loop" {
		t.Errorf("expected title 'Agent Loop', got %q", cycleWidget.Title)
	}

	// 3. verify initial html render
	html := cycleWidget.Render()
	if !strings.Contains(html, "agent loop") {
		t.Errorf("expected HTML to contain 'agent loop', got: %s", html)
	}
	if !strings.Contains(html, "run now") {
		t.Errorf("expected HTML to contain 'run now', got: %s", html)
	}

	// 4. verify update_fn returns json with __html
	upd := cycleWidget.Update()
	if !strings.Contains(upd, "__html") {
		t.Errorf("expected update to return JSON with __html, got: %s", upd)
	}
	if !strings.Contains(upd, "data-countdown-ts") {
		t.Errorf("expected update HTML to include data-countdown-ts, got: %s", upd)
	}

	// 5. verify click_handler toggles pause
	clickPayload := `{"act":"toggle","text":"❚❚ pause"}`
	res := cycleWidget.Click(clickPayload)
	if !strings.Contains(res, "__html") {
		t.Errorf("expected click to return updated HTML, got: %s", res)
	}
	if !strings.Contains(res, "resume") && !strings.Contains(res, "paused") {
		t.Errorf("expected paused widget HTML to contain pause indicator, got: %s", res)
	}

	// 6. Click unpause
	clickPayloadUnpause := `{"act":"toggle","text":"▶ resume"}`
	res2 := cycleWidget.Click(clickPayloadUnpause)
	if !strings.Contains(res2, "__html") {
		t.Errorf("expected unpause click to return updated HTML, got: %s", res2)
	}

	// 7. Click trigger
	clickPayloadTrigger := `{"act":"trigger","text":"▶ run now"}`
	res3 := cycleWidget.Click(clickPayloadTrigger)
	if !strings.Contains(res3, "__html") {
		t.Errorf("expected trigger click to return updated HTML, got: %s", res3)
	}

	// 8. verify plugin tools
	tools, err := addon.GetTools()
	if err != nil {
		t.Fatalf("failed to get tools: %v", err)
	}
	foundTool := false
	for _, tool := range tools {
		if tool["name"] == "agent_cycle_status" {
			foundTool = true
			break
		}
	}
	if !foundTool {
		t.Errorf("expected tools to contain 'agent_cycle_status', got: %+v", tools)
	}

	out, err := addon.CallTool("agent_cycle_status", "{}")
	if err != nil {
		t.Fatalf("CallTool agent_cycle_status failed: %v", err)
	}
	var statusMap map[string]any
	if err := json.Unmarshal([]byte(out), &statusMap); err != nil {
		t.Fatalf("failed to parse status JSON: %v (raw: %s)", err, out)
	}
	if enabled, ok := statusMap["enabled"].(bool); !ok || !enabled {
		t.Errorf("expected status enabled=true, got: %v", statusMap["enabled"])
	}
}
