package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestExtractFastUserFactsName(t *testing.T) {
	tmpDir := t.TempDir()
	ev := transportEvent{
		SenderID:       12345,
		SenderUsername: "rumpel_user",
		SenderName:     "Rumpel",
		Text:           "hi, my name is rumpelstiltskin!",
	}

	updated := extractFastUserFacts(tmpDir, ev)
	if !updated {
		t.Fatal("expected extractFastUserFacts to return true")
	}

	p := loadUserProfile(tmpDir, 12345)
	if p == nil {
		t.Fatal("user profile was not saved")
	}
	if p.RealName != "rumpelstiltskin" {
		t.Fatalf("expected real name 'rumpelstiltskin', got %q", p.RealName)
	}
	if p.Facts["name"] != "rumpelstiltskin" {
		t.Fatalf("expected fact 'name' = 'rumpelstiltskin', got %q", p.Facts["name"])
	}

	// verify context for prompt
	ctx := getUserProfileContext(tmpDir, 12345)
	if !strings.Contains(ctx, "rumpelstiltskin") {
		t.Fatalf("context must contain extracted name, got:\n%s", ctx)
	}
}

func TestExtractFastUserFactsDetails(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. age and city
	ev1 := transportEvent{
		SenderID: 777,
		Text:     "i am 28 years old and i live in London",
	}
	extractFastUserFacts(tmpDir, ev1)

	// 2. occupation
	ev2 := transportEvent{
		SenderID: 777,
		Text:     "i work as engineer",
	}
	extractFastUserFacts(tmpDir, ev2)

	p := loadUserProfile(tmpDir, 777)
	if p == nil {
		t.Fatal("profile not found")
	}
	if p.Facts["age"] != "28" {
		t.Errorf("expected age 28, got %q", p.Facts["age"])
	}
	if p.Facts["city"] != "London" {
		t.Errorf("expected city London, got %q", p.Facts["city"])
	}
	if p.Facts["job"] != "engineer" {
		t.Errorf("expected job engineer, got %q", p.Facts["job"])
	}
}

func TestForgetMe(t *testing.T) {
	tmpDir := t.TempDir()
	ev := transportEvent{
		SenderID: 999,
		Text:     "my name is rumpelstiltskin",
	}
	extractFastUserFacts(tmpDir, ev)

	if p := loadUserProfile(tmpDir, 999); p == nil {
		t.Fatal("profile not saved initially")
	}

	// deletion request
	evForget := transportEvent{
		SenderID: 999,
		Text:     "please, forget me",
	}
	extractFastUserFacts(tmpDir, evForget)

	if p := loadUserProfile(tmpDir, 999); p != nil {
		t.Fatalf("expected profile to be deleted, but found: %+v", p)
	}
	path := userProfilePath(tmpDir, 999)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file on disk was not removed")
	}
}

func TestMergeExtractedFacts(t *testing.T) {
	tmpDir := t.TempDir()
	rawJSON := `{"name": "Alex", "facts": {"pet": "Barsik the cat"}, "notes": ["likes spicy food"]}`

	mergeExtractedFacts(tmpDir, 555, "alex", "Alex", rawJSON, nil)

	p := loadUserProfile(tmpDir, 555)
	if p == nil {
		t.Fatal("profile was not created")
	}
	if p.RealName != "Alex" {
		t.Errorf("name = %q, want Alex", p.RealName)
	}
	if p.Facts["pet"] != "Barsik the cat" {
		t.Errorf("fact = %q, want Barsik the cat", p.Facts["pet"])
	}
	if len(p.Notes) != 1 || p.Notes[0] != "likes spicy food" {
		t.Errorf("notes = %#v", p.Notes)
	}
}

func TestFollowUpEventExtractionAndContext(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. fast pattern match for tomorrow event
	ev := transportEvent{
		SenderID:       101,
		SenderUsername: "bro_interview",
		SenderName:     "Bro",
		Text:           "tomorrow having an interview",
	}
	updated := extractFastUserFacts(tmpDir, ev)
	if !updated {
		t.Fatal("expected extractFastUserFacts to detect tomorrow event")
	}

	p := loadUserProfile(tmpDir, 101)
	if p == nil || len(p.FollowUps) == 0 {
		t.Fatal("expected FollowUps to have at least 1 item")
	}
	if !strings.Contains(p.FollowUps[0].Description, "interview") {
		t.Errorf("expected follow-up to mention interview, got %q", p.FollowUps[0].Description)
	}

	// 2. simulate due time after event
	p.FollowUps[0].CheckAfter = time.Now().Add(-1 * time.Hour) // already past
	if err := saveUserProfile(tmpDir, p); err != nil {
		t.Fatalf("failed to save profile: %v", err)
	}

	// 3. check hasDueFollowUp
	if !hasDueFollowUp(tmpDir, "bro_interview") {
		t.Fatal("expected hasDueFollowUp to return true")
	}

	// 4. check context for llm
	ctx := getUserProfileContext(tmpDir, 101)
	if !strings.Contains(ctx, "IMPORTANT DUE EVENTS") {
		t.Fatalf("expected context to contain due event notice, got:\n%s", ctx)
	}
	if !strings.Contains(ctx, "interview") {
		t.Fatalf("expected context to mention interview, got:\n%s", ctx)
	}

	// 5. mark as asked after bot message
	markUserFollowUpsAsked(tmpDir, "bro_interview")

	// 6. verify no longer reported as due
	if hasDueFollowUp(tmpDir, "bro_interview") {
		t.Fatal("expected hasDueFollowUp to be false after being asked")
	}
	pAfter := loadUserProfile(tmpDir, 101)
	if !pAfter.FollowUps[0].Asked {
		t.Fatal("expected Asked to be true")
	}
}

func TestDiverseEventsExtraction(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		text        string
		wantDescSub string
	}{
		{"tomorrow having driver exam", "driver exam"},
		{"after tomorrow going to dentist", "dentist"},
		{"tonight going on date", "date"},
	}

	for i, tt := range tests {
		uid := int64(200 + i)
		ev := transportEvent{
			SenderID:       uid,
			SenderUsername: fmt.Sprintf("user_%d", uid),
			Text:           tt.text,
		}
		if !extractFastUserFacts(tmpDir, ev) {
			t.Errorf("failed to extract event from %q", tt.text)
			continue
		}
		p := loadUserProfile(tmpDir, uid)
		if p == nil || len(p.FollowUps) == 0 {
			t.Errorf("no follow-ups for %q", tt.text)
			continue
		}
		if !strings.Contains(strings.ToLower(p.FollowUps[0].Description), strings.ToLower(tt.wantDescSub)) {
			t.Errorf("for %q, got description %q, want substring %q", tt.text, p.FollowUps[0].Description, tt.wantDescSub)
		}
	}
}

func TestImportantAIAction(t *testing.T) {
	tmpDir := t.TempDir()

	ev := transportEvent{
		SenderID:       555,
		SenderUsername: "bro_important",
		SenderName:     "Bro",
		Text:           "tomorrow having an important interview at yandex",
	}

	// 1. model response with |IMPORTANT| marker
	aiResp := "good luck! crossing fingers, you got this |IMPORTANT|"
	parts, actions := splitAIResponse(aiResp)

	// verify |IMPORTANT| is stripped from user-facing text
	if len(parts) != 1 || strings.Contains(parts[0], "|IMPORTANT|") {
		t.Fatalf("expected |IMPORTANT| to be stripped from parts, got: %#v", parts)
	}
	if parts[0] != "good luck! crossing fingers, you got this" {
		t.Errorf("unexpected part: %q", parts[0])
	}

	// 2. apply actions
	applyAIActions(tmpDir, ev, actions)

	// 3. verify follow-up and important note are saved in profile
	p := loadUserProfile(tmpDir, 555)
	if p == nil {
		t.Fatal("profile not created")
	}
	if len(p.FollowUps) == 0 {
		t.Fatal("expected follow-up to be created from |IMPORTANT|")
	}
	if !strings.Contains(p.FollowUps[0].Description, "interview") {
		t.Errorf("expected follow-up to contain 'interview', got %q", p.FollowUps[0].Description)
	}

	hasImportantNote := false
	for _, note := range p.Notes {
		if strings.Contains(note, "[important]") {
			hasImportantNote = true
			break
		}
	}
	if !hasImportantNote {
		t.Errorf("expected [important] note in profile, got: %#v", p.Notes)
	}

	// 4. explicit marker with argument
	ev2 := transportEvent{
		SenderID:       556,
		SenderUsername: "diploma_guy",
		SenderName:     "Student",
		Text:           "on wednesday i have something big",
	}
	aiResp2 := "got it, good luck on defense! |IMPORTANT=diploma defense|"
	parts2, actions2 := splitAIResponse(aiResp2)
	if strings.Contains(parts2[0], "|IMPORTANT") {
		t.Fatalf("expected marker to be stripped, got: %q", parts2[0])
	}
	applyAIActions(tmpDir, ev2, actions2)

	p2 := loadUserProfile(tmpDir, 556)
	if p2 == nil || len(p2.FollowUps) == 0 {
		t.Fatal("expected follow-up for student")
	}
	if p2.FollowUps[0].Description != "diploma defense" {
		t.Errorf("expected 'diploma defense', got %q", p2.FollowUps[0].Description)
	}
}
