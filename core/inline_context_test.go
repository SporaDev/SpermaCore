package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFindInviteLink(t *testing.T) {
	cases := map[string]string{
		"залетай https://t.me/+AbCdEf123":            "https://t.me/+AbCdEf123",
		"join https://t.me/joinchat/XyZ_123, please": "https://t.me/joinchat/XyZ_123",
		"tg://join?invite=QwErTy":                    "tg://join?invite=QwErTy",
		"look https://t.me/somechannel":              "",
		"just a /plus+ sign":                         "",
		"no links here":                              "",
	}
	for input, want := range cases {
		if got := findInviteLink(input); got != want {
			t.Errorf("findInviteLink(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestInlineEventContext(t *testing.T) {
	ev := transportEvent{
		ChatID:         42,
		MessageID:      7,
		ViaBotName:     "Gif Bot",
		ViaBotUsername: "gif_bot",
		Buttons: []InlineButton{
			{Index: 1, Text: "Да", Type: "callback", DataText: "yes"},
			{Index: 2, Text: "Открыть", Type: "url", URL: "https://example.com"},
		},
		Text: "выбирай https://t.me/+joinhash",
	}
	ctx := inlineEventContext(ev)
	for _, want := range []string{
		"INLINE BOT ATTRIBUTION", "@gif_bot",
		"INLINE KEYBOARD ON CURRENT MESSAGE", "1. \"Да\" [callback] data=yes",
		"press_button(chat_id=42, message_id=7",
		"GROUP INVITE LINK", "check_invite", "join_chat",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("inline context missing %q:\n%s", want, ctx)
		}
	}
}

func TestCallbackEventText(t *testing.T) {
	ev := transportEvent{
		ButtonText:       "Продолжить",
		CallbackDataText: "next_page",
		Text:             "меню",
	}
	text := callbackEventText(ev)
	for _, want := range []string{"inline callback", "Продолжить", "next_page", "меню"} {
		if !strings.Contains(text, want) {
			t.Errorf("callback text missing %q: %q", want, text)
		}
	}

	ctx := inlineEventContext(transportEvent{
		IsCallback:       true,
		CallbackQueryID:  991,
		ButtonText:       "Продолжить",
		CallbackData:     "bmV4dA==",
		CallbackDataText: "next",
	})
	for _, want := range []string{"INLINE CALLBACK", "callback_query_id = 991", "answer_callback"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("callback context missing %q:\n%s", want, ctx)
		}
	}
}

func TestTransportEventInlineJSON(t *testing.T) {
	raw := `{
		"type":"new_message","transport":"telegram","chat_id":-1001,"message_id":5,
		"sender_id":10,"text":"hi",
		"via_bot_id":777,"via_bot_name":"Pic Bot","via_bot_username":"pic_bot",
		"buttons":[{"index":1,"row":0,"col":0,"text":"Готово","type":"callback","data":"b2s=","data_text":"ok"}]
	}`
	var ev transportEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.ViaBotID != 777 || ev.ViaBotUsername != "pic_bot" {
		t.Errorf("via bot not decoded: %+v", ev)
	}
	if len(ev.Buttons) != 1 || ev.Buttons[0].Text != "Готово" || ev.Buttons[0].DataText != "ok" {
		t.Errorf("buttons not decoded: %+v", ev.Buttons)
	}
	if !strings.Contains(inlineEventContext(ev), "@pic_bot") {
		t.Errorf("attribution missing from context")
	}
}

func TestNormalizeIngestEventCallback(t *testing.T) {
	raw := `{
		"type":"callback_query","transport":"telegram","chat_id":-1001,"message_id":9,
		"sender_id":10,"sender_name":"Вася","sender_username":"vasya",
		"callback_query_id":4242,"callback_data":"bmV4dA==","callback_data_text":"next",
		"button_text":"Дальше","text":"меню бота",
		"via_bot_id":888,"via_bot_name":"Menu Bot","via_bot_username":"menu_bot"
	}`
	var ev transportEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	ev, ok := normalizeIngestEvent(ev)
	if !ok {
		t.Fatal("callback event was rejected")
	}
	if !ev.IsCallback {
		t.Error("callback flag not set")
	}
	if !strings.Contains(ev.Text, "Дальше") {
		t.Errorf("synthesised text missing button: %q", ev.Text)
	}
	for _, want := range []string{"INLINE CALLBACK", "4242", "@menu_bot"} {
		if !strings.Contains(ev.InlineContext, want) {
			t.Errorf("inline context missing %q:\n%s", want, ev.InlineContext)
		}
	}

	var plain transportEvent
	if err := json.Unmarshal([]byte(`{"type":"user_status","chat_id":1}`), &plain); err != nil {
		t.Fatal(err)
	}
	if _, ok := normalizeIngestEvent(plain); ok {
		t.Error("unknown event type should be rejected")
	}
}

func TestInlineEventContextPoll(t *testing.T) {
	raw := `{
		"type":"new_message","transport":"telegram","chat_id":-1001,"message_id":55,
		"sender_id":10,"media_type":"poll","text":"Куда идём?",
		"poll":{
			"question":"Куда идём?",
			"options":[
				{"index":0,"text":"В кино","voter_count":3,"is_chosen":true},
				{"index":1,"text":"В бар","voter_count":5}
			],
			"total_voter_count":8,"is_anonymous":true,"is_closed":false
		}
	}`
	var ev transportEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Poll == nil || len(ev.Poll.Options) != 2 {
		t.Fatalf("poll not decoded: %+v", ev.Poll)
	}
	ctx := inlineEventContext(ev)
	for _, want := range []string{
		"TELEGRAM POLL ON CURRENT MESSAGE",
		"id=0. В кино", "id=1. В бар",
		"[already chosen by you]",
		"vote_poll(chat_id=-1001, message_id=55",
	} {
		if !strings.Contains(ctx, want) {
			t.Errorf("poll context missing %q:\n%s", want, ctx)
		}
	}

	ev.Poll.IsClosed = true
	closed := inlineEventContext(ev)
	if !strings.Contains(closed, "closed") || strings.Contains(closed, "vote_poll(") {
		t.Errorf("closed poll must not advertise voting:\n%s", closed)
	}
}
