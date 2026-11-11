package main

import (
	"testing"
	"time"
)

func testGroupPolicy() groupPolicy {
	return groupPolicy{
		Enabled:           true,
		ReplyChance:       0,
		Priority1Chance:   0,
		Priority2Chance:   1,
		Priority3Always:   true,
		Cooldown:          0,
		EngagementWindow:  0,
		FollowupWindow:    0,
		MaxRepliesPerHour: 0,
		MinAmbientChars:   8,
		ActivePhrases:     []string{"bot"},
		PriorityUsers:     map[string]int{"admin": 3},
		IgnoredUsers:      map[string]bool{},
	}
}

func installTestGroupPolicy(t *testing.T, p groupPolicy) {
	t.Helper()
	groupPolicyMu.Lock()
	groupPolicyState = p
	groupPolicyMu.Unlock()
	groupRuntimeMu.Lock()
	groupChats = map[int64]*chatActivity{}
	groupRuntimeMu.Unlock()
}

func TestGroupDirectActivationAlwaysResponds(t *testing.T) {
	p := testGroupPolicy()
	p.ReplyChance = 0
	installTestGroupPolicy(t, p)

	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, Text: "bot, are you there?"})
	if !ok || mode != "direct" {
		t.Fatalf("direct activation: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupReplyToBotIsDirect(t *testing.T) {
	installTestGroupPolicy(t, testGroupPolicy())

	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, Text: "what do you think", ReplyFromSelf: true})
	if !ok || mode != "direct" {
		t.Fatalf("reply to bot: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupLowSignalIgnored(t *testing.T) {
	p := testGroupPolicy()
	p.ReplyChance = 1
	installTestGroupPolicy(t, p)

	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, Text: "ok"})
	if ok || mode != "low_signal" {
		t.Fatalf("low signal: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupPriorityThreeAlwaysResponds(t *testing.T) {
	installTestGroupPolicy(t, testGroupPolicy())

	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, SenderUsername: "Admin", Text: "ok"})
	if !ok || mode != "priority" {
		t.Fatalf("priority 3: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupCooldownAndRateLimit(t *testing.T) {
	p := testGroupPolicy()
	p.ReplyChance = 1
	p.Cooldown = time.Minute
	installTestGroupPolicy(t, p)
	ev := transportEvent{ChatID: -1001, Text: "hello, what is new"}

	noteBotReply(ev)
	ok, mode := groupShouldRespond(ev)
	if ok || mode != "cooldown" {
		t.Fatalf("cooldown: got ok=%v mode=%q", ok, mode)
	}

	p.Cooldown = 0
	p.MaxRepliesPerHour = 1
	installTestGroupPolicy(t, p)
	noteBotReply(ev)
	ok, mode = groupShouldRespond(ev)
	if ok || mode != "rate_limit" {
		t.Fatalf("rate limit: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupDoesNotInterruptOthers(t *testing.T) {
	p := testGroupPolicy()
	p.ReplyChance = 1
	installTestGroupPolicy(t, p)

	ok, mode := groupShouldRespond(transportEvent{
		ChatID: -1001, Text: "i am thinking about new graphics cards", ReplyToID: 55,
	})
	if ok || mode != "reply_to_other" {
		t.Fatalf("reply to other: got ok=%v mode=%q", ok, mode)
	}

	ok, mode = groupShouldRespond(transportEvent{
		ChatID: -1001, Text: "@petya i am thinking about new graphics cards",
	})
	if ok || mode != "addressed_to_other" {
		t.Fatalf("addressed to other: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupAmbientChance(t *testing.T) {
	p := testGroupPolicy()
	p.ReplyChance = 1
	installTestGroupPolicy(t, p)

	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, Text: "i am thinking about new graphics cards"})
	if !ok || mode != "ambient" {
		t.Fatalf("ambient: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupActivationRussianYo(t *testing.T) {
	p := testGroupPolicy()
	p.ActivePhrases = []string{"spermacore", "assistant"}
	installTestGroupPolicy(t, p)

	if !hasActivationPhrase("hello, spermacore!", "spermacore") {
		t.Fatalf("hasActivationPhrase failed for 'spermacore'")
	}
	if !hasActivationPhrase("is ASSISTANT online?", "assistant") {
		t.Fatalf("hasActivationPhrase failed for 'assistant'")
	}
}

func TestGroupUsernameMentionIsDirect(t *testing.T) {
	p := testGroupPolicy()
	p.BotUsernames = []string{"test_bot"}
	installTestGroupPolicy(t, p)

	// Mention in middle of text
	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, Text: "listen @test_bot, explain life"})
	if !ok || mode != "direct" {
		t.Fatalf("mention in text: got ok=%v mode=%q", ok, mode)
	}

	// Mention with colon
	ok, mode = groupShouldRespond(transportEvent{ChatID: -1001, Text: "@test_bot: are you there?"})
	if !ok || mode != "direct" {
		t.Fatalf("mention with colon: got ok=%v mode=%q", ok, mode)
	}

	// Dynamic self username
	ok, mode = groupShouldRespond(transportEvent{ChatID: -1001, Text: "@dynamic_bot hello", SelfUsername: "dynamic_bot"})
	if !ok || mode != "direct" {
		t.Fatalf("dynamic self username: got ok=%v mode=%q", ok, mode)
	}

	// Mention of another user should not activate and should be addressed_to_other
	ok, mode = groupShouldRespond(transportEvent{ChatID: -1001, Text: "@other_user hello"})
	if ok || mode != "addressed_to_other" {
		t.Fatalf("mention of another user: got ok=%v mode=%q", ok, mode)
	}
}

func TestGroupReplyToBotViaRememberedMessage(t *testing.T) {
	installTestGroupPolicy(t, testGroupPolicy())

	rememberBotSentMessage(-1001, 777)
	ok, mode := groupShouldRespond(transportEvent{ChatID: -1001, Text: "you are right", ReplyToID: 777})
	if !ok || mode != "direct" {
		t.Fatalf("reply to remembered bot message: got ok=%v mode=%q", ok, mode)
	}
}

func TestContainsMentionBoundary(t *testing.T) {
	if !containsMention("@test_bot hello", "test_bot") {
		t.Fatal("expected match at start")
	}
	if !containsMention("hello @test_bot!", "test_bot") {
		t.Fatal("expected match with punctuation")
	}
	if containsMention("hello @test_bot_extra", "test_bot") {
		t.Fatal("should not match prefix substring of another username")
	}
	if containsMention("email@test_bot.com", "test_bot") {
		t.Fatal("should not match email address")
	}
}
