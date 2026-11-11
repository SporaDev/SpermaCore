package main

import (
	"reflect"
	"testing"
)

func TestParseAIActions(t *testing.T) {
	tests := []struct {
		input       string
		expectedTxt string
		expectedAct []aiAction
	}{
		{
			input:       "hello! |PHOTO=data/meme.jpg| check this pic",
			expectedTxt: "hello!  check this pic",
			expectedAct: []aiAction{{Name: "PHOTO", Value: "data/meme.jpg"}},
		},
		{
			input:       "|GIF=https://example.com/cat.gif::funny cat| lol",
			expectedTxt: "lol",
			expectedAct: []aiAction{{Name: "GIF", Value: "https://example.com/cat.gif::funny cat"}},
		},
		{
			input:       "|REACT=🔥| great job",
			expectedTxt: "great job",
			expectedAct: []aiAction{{Name: "REACT", Value: "🔥"}},
		},
		{
			input:       "|REACT=5368324170671202288| custom reaction",
			expectedTxt: "custom reaction",
			expectedAct: []aiAction{{Name: "REACT", Value: "5368324170671202288"}},
		},
		{
			input:       "hey, check out this voice message! |VOICE|",
			expectedTxt: "hey, check out this voice message!",
			expectedAct: []aiAction{{Name: "VOICE", Value: ""}},
		},
		{
			input:       "|VOICE=listen to me closely bro| text note",
			expectedTxt: "text note",
			expectedAct: []aiAction{{Name: "VOICE", Value: "listen to me closely bro"}},
		},
		{
			input:       "plain text without actions",
			expectedTxt: "plain text without actions",
			expectedAct: nil,
		},
		{
			input:       "got it, good luck |important|",
			expectedTxt: "got it, good luck",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: ""}},
		},
		{
			input:       "good luck |Important = diploma defense|",
			expectedTxt: "good luck",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: "diploma defense"}},
		},
		{
			// models often forget the trailing pipe on the last marker
			input:       "good luck, you got this |IMPORTANT=job interview tomorrow",
			expectedTxt: "good luck, you got this",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: "job interview tomorrow"}},
		},
		{
			input:       "wait a sec | split | nah",
			expectedTxt: "wait a sec  nah",
			expectedAct: []aiAction{{Name: "SPLIT", Value: ""}},
		},
		{
			input:       "keep |hello| untouched",
			expectedTxt: "keep |hello| untouched",
			expectedAct: nil,
		},
		{
			input:       "|IMPORTANTLY| not a marker",
			expectedTxt: "|IMPORTANTLY| not a marker",
			expectedAct: nil,
		},
		{
			input:       "good luck |IMPORTANT: interview tomorrow|",
			expectedTxt: "good luck",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: "interview tomorrow"}},
		},
		{
			input:       "see you |ВАЖНО: поездка в Питер|",
			expectedTxt: "see you",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: "поездка в Питер"}},
		},
		{
			input:       "hello [IMPORTANT=medical checkup]",
			expectedTxt: "hello",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: "medical checkup"}},
		},
		{
			input:       "good luck **|IMPORTANT: exam|**",
			expectedTxt: "good luck",
			expectedAct: []aiAction{{Name: "IMPORTANT", Value: "exam"}},
		},
	}

	for _, tt := range tests {
		clean, acts := parseAIActions(tt.input)
		if clean != tt.expectedTxt {
			t.Errorf("parseAIActions text mismatch: got %q, want %q", clean, tt.expectedTxt)
		}
		if !reflect.DeepEqual(acts, tt.expectedAct) {
			t.Errorf("parseAIActions actions mismatch: got %+v, want %+v", acts, tt.expectedAct)
		}
	}
}

func TestSplitAIResponse(t *testing.T) {
	input := "first thought|SPLIT|second thought |REACT=👍|"
	parts, acts := splitAIResponse(input)

	if len(parts) != 2 || parts[0] != "first thought" || parts[1] != "second thought" {
		t.Errorf("unexpected parts: %v", parts)
	}
	if len(acts) != 1 || acts[0].Name != "REACT" || acts[0].Value != "👍" {
		t.Errorf("unexpected actions: %+v", acts)
	}
}

func TestSplitAIResponseTolerantMarkers(t *testing.T) {
	tests := []struct {
		input string
		want  []string
		acts  []aiAction
	}{
		{
			input: "first |split| second",
			want:  []string{"first", "second"},
		},
		{
			input: "first | SPLIT | second |IMPORTANT=exam tomorrow",
			want:  []string{"first", "second"},
			acts:  []aiAction{{Name: "IMPORTANT", Value: "exam tomorrow"}},
		},
		{
			input: "first|NEXT|second|next|third",
			want:  []string{"first", "second", "third"},
		},
		{
			input: "first **|SPLIT|** second",
			want:  []string{"first", "second"},
		},
		{
			input: "first [SPLIT] second",
			want:  []string{"first", "second"},
		},
		{
			input: "first |СПЛИТ| second |ВАЖНО: встреча в 5|",
			want:  []string{"first", "second"},
			acts:  []aiAction{{Name: "IMPORTANT", Value: "встреча в 5"}},
		},
		{
			input: "[12:34] first thought|SPLIT|12:35 - second thought",
			want:  []string{"first thought", "second thought"},
		},
	}

	for _, tt := range tests {
		parts, acts := splitAIResponse(tt.input)
		if !reflect.DeepEqual(parts, tt.want) {
			t.Errorf("splitAIResponse(%q) parts = %v, want %v", tt.input, parts, tt.want)
		}
		if !reflect.DeepEqual(acts, tt.acts) {
			t.Errorf("splitAIResponse(%q) actions = %+v, want %+v", tt.input, acts, tt.acts)
		}
	}
}

func TestSanitizedMessageToolCalls(t *testing.T) {
	args := map[string]any{"chat_id": int64(1), "text": "one|SPLIT|two |important|"}

	calls, actions := sanitizedMessageToolCalls("send_message", args)
	if len(calls) != 2 {
		t.Fatalf("expected 2 send calls, got %d: %#v", len(calls), calls)
	}
	if calls[0]["text"] != "one" || calls[1]["text"] != "two" {
		t.Errorf("unexpected call texts: %#v", calls)
	}
	if calls[0]["chat_id"] != int64(1) || calls[1]["chat_id"] != int64(1) {
		t.Errorf("chat_id was not preserved: %#v", calls)
	}
	if !reflect.DeepEqual(actions, []aiAction{{Name: "IMPORTANT", Value: ""}}) {
		t.Errorf("unexpected actions: %+v", actions)
	}

	calls, _ = sanitizedMessageToolCalls("edit_message", map[string]any{"text": "one|SPLIT|two"})
	if len(calls) != 1 || calls[0]["text"] != "one\ntwo" {
		t.Errorf("edit_message should not split into two calls: %#v", calls)
	}

	if calls, actions := sanitizedMessageToolCalls("send_message", map[string]any{"text": "plain"}); calls != nil || actions != nil {
		t.Errorf("plain text should be passed through, got %#v %+v", calls, actions)
	}

	calls, actions = sanitizedMessageToolCalls("send_message", map[string]any{"text": "|IMPORTANT=exam|"})
	if len(calls) != 0 {
		t.Errorf("marker-only message should not be sent: %#v", calls)
	}
	if len(actions) != 1 || actions[0].Name != "IMPORTANT" {
		t.Errorf("marker-only actions were not extracted: %+v", actions)
	}
}

func TestApplyAIActionsNoPanic(t *testing.T) {
	ev := transportEvent{
		Transport:      "telegram",
		ChatID:         -100123456789,
		MessageID:      42,
		SenderUsername: "testuser",
	}

	acts := []aiAction{
		{Name: "SETPRIORITY", Value: "2"},
		{Name: "UNIGNORE", Value: ""},
		{Name: "REACT", Value: "🤡"},
		{Name: "PHOTO", Value: "dummy.png::caption"},
	}

	// verify applyAIActions handles missing transport without panicking
	applyAIActions(t.TempDir(), ev, acts)
}

func TestApplyAIBlockUnblock(t *testing.T) {
	ev := transportEvent{
		Transport:      "telegram",
		ChatID:         -100999,
		SenderID:       12345,
		SenderUsername: "annoying_troll",
	}

	// Initially not ignored
	if isIgnoredUser(ev) {
		t.Fatalf("user should not be ignored initially")
	}

	// Apply BLOCK
	applyAIActions(t.TempDir(), ev, []aiAction{{Name: "BLOCK"}})
	if !isIgnoredUser(ev) {
		t.Errorf("user should be ignored after BLOCK")
	}

	// Also check event with only SenderID
	evNoUsername := transportEvent{
		SenderID: 12345,
	}
	if !isIgnoredUser(evNoUsername) {
		t.Errorf("user with SenderID should be ignored after BLOCK")
	}

	// Apply UNBLOCK
	applyAIActions(t.TempDir(), ev, []aiAction{{Name: "UNBLOCK", Value: "annoying_troll"}})
	if isIgnoredUser(ev) {
		t.Errorf("user should not be ignored after UNBLOCK")
	}

	// Apply DELETECHAT (verify no panic)
	applyAIActions(t.TempDir(), ev, []aiAction{{Name: "DELETECHAT"}, {Name: "DELETECHAT", Value: "-100999"}})
}
