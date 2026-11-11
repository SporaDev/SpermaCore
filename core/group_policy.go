package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// [groups] defaults
var groupPolicyDefaults = map[string]any{
	"enabled":                   true,
	"reply_chance":              0.06,
	"priority1_chance":          0.12,
	"priority2_chance":          0.55,
	"priority3_always_reply":    true,
	"cooldown_seconds":          45,
	"engagement_window_seconds": 4,
	"followup_window_seconds":   180,
	"max_replies_per_hour":      8,
	"min_ambient_chars":         8,
	"bot_usernames":             []string{},
	"active_phrases_file":       "data/active-phrases.json",
}

// group policy config
type groupPolicy struct {
	Enabled           bool
	ReplyChance       float64
	Priority1Chance   float64
	Priority2Chance   float64
	Priority3Always   bool
	Cooldown          time.Duration
	EngagementWindow  time.Duration
	FollowupWindow    time.Duration
	MaxRepliesPerHour int
	MinAmbientChars   int
	BotUsernames      []string
	PriorityUsers     map[string]int
	ActivePhrases     []string
	IgnoredUsers      map[string]bool
}

// chat activity tracker
type chatActivity struct {
	lastReply    time.Time
	engagedUntil time.Time
	replies      []time.Time
}

var (
	groupPolicyMu    sync.RWMutex
	groupPolicyState groupPolicy

	groupRuntimeMu sync.Mutex
	groupChats     = map[int64]*chatActivity{}
	groupRNG       = rand.New(rand.NewSource(time.Now().UnixNano()))
)

func loadGroupPolicy(root string, section map[string]any, log *Logger) {
	policy := groupPolicy{
		Enabled:           true,
		ReplyChance:       0.06,
		Priority1Chance:   0.12,
		Priority2Chance:   0.55,
		Priority3Always:   true,
		Cooldown:          45 * time.Second,
		EngagementWindow:  4 * time.Second,
		FollowupWindow:    180 * time.Second,
		MaxRepliesPerHour: 8,
		MinAmbientChars:   8,
		PriorityUsers:     make(map[string]int),
		IgnoredUsers:      make(map[string]bool),
	}
	if section != nil {
		if v, ok := section["enabled"].(bool); ok {
			policy.Enabled = v
		}
		if v, ok := groupFloat(section, "reply_chance"); ok {
			policy.ReplyChance = clamp01(v)
		}
		if v, ok := groupFloat(section, "priority1_chance"); ok {
			policy.Priority1Chance = clamp01(v)
		}
		if v, ok := groupFloat(section, "priority2_chance"); ok {
			policy.Priority2Chance = clamp01(v)
		}
		if v, ok := section["priority3_always_reply"].(bool); ok {
			policy.Priority3Always = v
		}
		if v, ok := groupInt(section, "cooldown_seconds"); ok && v >= 0 {
			policy.Cooldown = time.Duration(v) * time.Second
		}
		if v, ok := groupInt(section, "engagement_window_seconds"); ok && v >= 0 {
			policy.EngagementWindow = time.Duration(v) * time.Second
		}
		if v, ok := groupInt(section, "followup_window_seconds"); ok && v >= 0 {
			policy.FollowupWindow = time.Duration(v) * time.Second
		}
		if v, ok := groupInt(section, "max_replies_per_hour"); ok && v >= 0 {
			policy.MaxRepliesPerHour = int(v)
		}
		if v, ok := groupInt(section, "min_ambient_chars"); ok && v >= 0 {
			policy.MinAmbientChars = int(v)
		}
		policy.BotUsernames = groupStrings(section, "bot_usernames")
		if v, ok := section["priority_users"].(map[string]any); ok {
			for user, level := range v {
				if n, ok := groupInt(map[string]any{"v": level}, "v"); ok && n >= 0 && n <= 3 {
					policy.PriorityUsers[normalizeUser(user)] = int(n)
				}
			}
		}
		if v, ok := section["ignored_users"].(map[string]any); ok {
			for user, ignored := range v {
				if value, ok := ignored.(bool); ok && value {
					policy.IgnoredUsers[normalizeUser(user)] = true
				}
			}
		}
		if items := groupStrings(section, "active_phrases"); len(items) > 0 {
			policy.ActivePhrases = items
		}
		if file, ok := section["active_phrases_file"].(string); ok && file != "" {
			path := file
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, file)
			}
			if _, err := os.Stat(path); err != nil {
				path = filepath.Join(root, "data", filepath.Base(file))
			}
			if data, err := os.ReadFile(path); err == nil {
				data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
				var file struct {
					ActivePhrases []string `json:"active_phrases"`
				}
				if err := json.Unmarshal(data, &file); err == nil {
					policy.ActivePhrases = file.ActivePhrases
				} else {
					log.Warn("group policy: invalid phrases file: %v", err)
				}
			} else {
				log.Warn("group policy: %v", err)
			}
		}
	}
	for i, name := range policy.BotUsernames {
		policy.BotUsernames[i] = normalizeUser(name)
	}
	groupPolicyMu.Lock()
	groupPolicyState = policy
	groupPolicyMu.Unlock()
	log.Info("group policy: enabled=%v reply_chance=%.2f priority=%.2f/%.2f cooldown=%v limit=%d/h phrases=%d",
		policy.Enabled, policy.ReplyChance, policy.Priority1Chance, policy.Priority2Chance,
		policy.Cooldown, policy.MaxRepliesPerHour, len(policy.ActivePhrases))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func groupInt(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

func groupFloat(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

func groupStrings(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func normalizeUser(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "@"))
}

func userPriority(ev transportEvent) int {
	groupPolicyMu.RLock()
	defer groupPolicyMu.RUnlock()
	if p, ok := groupPolicyState.PriorityUsers[normalizeUser(ev.SenderUsername)]; ok {
		return p
	}
	return 0
}

func isPriorityUser(ev transportEvent) bool { return userPriority(ev) > 0 }

func isIgnoredUser(ev transportEvent) bool {
	groupPolicyMu.RLock()
	defer groupPolicyMu.RUnlock()
	username := normalizeUser(ev.SenderUsername)
	if username != "" && groupPolicyState.IgnoredUsers[username] {
		return true
	}
	if ev.SenderID != 0 && groupPolicyState.IgnoredUsers[fmt.Sprintf("id:%d", ev.SenderID)] {
		return true
	}
	return false
}

func setUserIgnored(username string, ignored bool) {
	username = normalizeUser(username)
	if username == "" {
		return
	}
	groupPolicyMu.Lock()
	if groupPolicyState.IgnoredUsers == nil {
		groupPolicyState.IgnoredUsers = make(map[string]bool)
	}
	if ignored {
		groupPolicyState.IgnoredUsers[username] = true
	} else {
		delete(groupPolicyState.IgnoredUsers, username)
	}
	groupPolicyMu.Unlock()
}

func setUserPriority(username string, level int) {
	username = normalizeUser(username)
	if username == "" || level < 0 || level > 3 {
		return
	}
	groupPolicyMu.Lock()
	if groupPolicyState.PriorityUsers == nil {
		groupPolicyState.PriorityUsers = make(map[string]int)
	}
	groupPolicyState.PriorityUsers[username] = level
	groupPolicyMu.Unlock()
}

// register bot username
func RegisterBotUsername(username string) {
	u := normalizeUser(username)
	if u == "" {
		return
	}
	groupPolicyMu.Lock()
	defer groupPolicyMu.Unlock()
	for _, existing := range groupPolicyState.BotUsernames {
		if strings.EqualFold(existing, u) {
			return
		}
	}
	groupPolicyState.BotUsernames = append(groupPolicyState.BotUsernames, u)
}

// register bot name
func RegisterBotName(name string) {
	n := strings.TrimSpace(name)
	if n == "" {
		return
	}
	groupPolicyMu.Lock()
	defer groupPolicyMu.Unlock()
	for _, existing := range groupPolicyState.ActivePhrases {
		if strings.EqualFold(existing, n) {
			return
		}
	}
	groupPolicyState.ActivePhrases = append(groupPolicyState.ActivePhrases, n)
}

var (
	ownMessagesMu sync.RWMutex
	ownMessages   = make(map[string]time.Time)
)

func rememberBotSentMessage(chatID int64, messageID int) {
	if chatID == 0 || messageID <= 0 {
		return
	}
	key := fmt.Sprintf("%d:%d", chatID, messageID)
	ownMessagesMu.Lock()
	defer ownMessagesMu.Unlock()
	ownMessages[key] = time.Now()
	if len(ownMessages) > 10000 {
		cutoff := time.Now().Add(-7 * 24 * time.Hour)
		for k, t := range ownMessages {
			if t.Before(cutoff) {
				delete(ownMessages, k)
			}
		}
	}
}

func isBotSentMessage(chatID int64, messageID int) bool {
	if chatID == 0 || messageID <= 0 {
		return false
	}
	key := fmt.Sprintf("%d:%d", chatID, messageID)
	ownMessagesMu.RLock()
	defer ownMessagesMu.RUnlock()
	_, ok := ownMessages[key]
	return ok
}

// check @username mention with word boundary
func containsMention(text, username string) bool {
	u := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if u == "" {
		return false
	}
	target := "@" + u
	t := strings.ToLower(text)
	idx := 0
	for {
		pos := strings.Index(t[idx:], target)
		if pos == -1 {
			return false
		}
		actualPos := idx + pos
		endPos := actualPos + len(target)

		validStart := (actualPos == 0)
		if !validStart {
			prevRune := rune(t[actualPos-1])
			isUsernameChar := (prevRune >= 'a' && prevRune <= 'z') ||
				(prevRune >= 'A' && prevRune <= 'Z') ||
				(prevRune >= '0' && prevRune <= '9') ||
				prevRune == '_'
			validStart = !isUsernameChar
		}

		validEnd := (endPos >= len(t))
		if !validEnd {
			nextRune := rune(t[endPos])
			isUsernameChar := (nextRune >= 'a' && nextRune <= 'z') ||
				(nextRune >= 'A' && nextRune <= 'Z') ||
				(nextRune >= '0' && nextRune <= '9') ||
				nextRune == '_'
			validEnd = !isUsernameChar
		}

		if validStart && validEnd {
			return true
		}
		idx = actualPos + len(target)
	}
}

func mentionsBot(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	groupPolicyMu.RLock()
	defer groupPolicyMu.RUnlock()
	for _, name := range groupPolicyState.BotUsernames {
		if name != "" && (containsMention(t, name) || hasActivationPhrase(t, name)) {
			return true
		}
	}
	for _, phrase := range groupPolicyState.ActivePhrases {
		p := normalizeUser(phrase)
		if p != "" && (containsMention(t, p) || strings.Contains(t, "@"+p)) {
			return true
		}
	}
	return false
}

func hasActivationPhrase(text, phrase string) bool {
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !('а' <= r && r <= 'я') && !('А' <= r && r <= 'Я') &&
			r != 'ё' && r != 'Ё' &&
			!('a' <= r && r <= 'z') && !('A' <= r && r <= 'Z') &&
			!('0' <= r && r <= '9') && r != '-'
	}) {
		if strings.EqualFold(strings.Trim(word, "-"), phrase) {
			return true
		}
	}
	return false
}

func groupActivated(text string) bool {
	t := strings.ToLower(text)
	groupPolicyMu.RLock()
	defer groupPolicyMu.RUnlock()
	for _, p := range groupPolicyState.ActivePhrases {
		p = strings.TrimSpace(strings.ToLower(p))
		if strings.HasSuffix(p, "-*") {
			if strings.HasPrefix(t, strings.TrimSuffix(p, "-*")) {
				return true
			}
		} else if p != "" && hasActivationPhrase(t, p) {
			return true
		}
	}
	for _, name := range groupPolicyState.BotUsernames {
		if name != "" && hasActivationPhrase(t, name) {
			return true
		}
	}
	return false
}

// check direct activation (mention/reply/name)
func isDirectActivation(ev transportEvent) bool {
	if ev.ReplyFromSelf || (ev.ReplyToID > 0 && isBotSentMessage(ev.ChatID, ev.ReplyToID)) {
		return true
	}
	if ev.SelfUsername != "" && containsMention(ev.Text, ev.SelfUsername) {
		return true
	}
	if ev.SelfName != "" && hasActivationPhrase(strings.ToLower(ev.Text), strings.ToLower(ev.SelfName)) {
		return true
	}
	return groupActivated(ev.Text) || mentionsBot(ev.Text)
}

// filter short/low-signal texts
func lowSignalText(text string, minChars int) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	meaningful := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			meaningful++
		}
	}
	return meaningful < minChars
}

// determine if bot should respond in group
func groupShouldRespond(ev transportEvent) (bool, string) {
	if ev.ChatID >= 0 {
		return true, "private"
	}
	groupPolicyMu.RLock()
	p := groupPolicyState
	groupPolicyMu.RUnlock()
	if !p.Enabled {
		return true, "direct"
	}
	if isDirectActivation(ev) {
		groupMarkEngaged(ev.ChatID, p.EngagementWindow)
		return true, "direct"
	}

	priority := userPriority(ev)
	if priority >= 3 && p.Priority3Always {
		groupMarkEngaged(ev.ChatID, p.EngagementWindow)
		return true, "priority"
	}
	if priority == 0 && ev.ReplyToID > 0 && !ev.ReplyFromSelf {
		return false, "reply_to_other"
	}
	if priority == 0 && !mentionsBot(ev.Text) && strings.Contains(ev.Text, "@") {
		return false, "addressed_to_other"
	}

	now := time.Now()
	groupRuntimeMu.Lock()
	defer groupRuntimeMu.Unlock()

	act := groupChats[ev.ChatID]
	if act == nil {
		act = &chatActivity{}
		groupChats[ev.ChatID] = act
	}
	groupChatsIdle.touch(ev.ChatID)

	cutoff := now.Add(-time.Hour)
	kept := act.replies[:0]
	for _, ts := range act.replies {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	act.replies = kept

	if p.MaxRepliesPerHour > 0 && len(act.replies) >= p.MaxRepliesPerHour {
		return false, "rate_limit"
	}
	if act.engagedUntil.After(now) {
		act.engagedUntil = now.Add(p.EngagementWindow)
		return true, "ambient"
	}
	if p.Cooldown > 0 && !act.lastReply.IsZero() && now.Sub(act.lastReply) < p.Cooldown {
		return false, "cooldown"
	}
	if priority == 0 && lowSignalText(ev.Text, p.MinAmbientChars) {
		return false, "low_signal"
	}

	chance := p.ReplyChance
	switch priority {
	case 1:
		chance = p.Priority1Chance
	case 2:
		chance = p.Priority2Chance
	}
	if p.FollowupWindow > 0 && !act.lastReply.IsZero() && now.Sub(act.lastReply) < p.FollowupWindow {
		chance *= 2.2
	}
	if strings.Contains(ev.Text, "?") {
		chance *= 1.6
	}
	if len([]rune(ev.Text)) > 60 {
		chance *= 1.15
	}
	chance = clamp01(chance)
	if chance <= 0 {
		return false, "chance"
	}
	if groupRNG.Float64() >= chance {
		return false, "chance"
	}
	act.engagedUntil = now.Add(p.EngagementWindow)
	if priority > 0 {
		return true, "priority"
	}
	return true, "ambient"
}

// extend engagement window
func groupMarkEngaged(chatID int64, window time.Duration) {
	if window <= 0 {
		return
	}
	now := time.Now()
	groupRuntimeMu.Lock()
	act := groupChats[chatID]
	if act == nil {
		act = &chatActivity{}
		groupChats[chatID] = act
	}
	groupChatsIdle.touch(chatID)
	act.engagedUntil = now.Add(window)
	groupRuntimeMu.Unlock()
}

func activationOrDefault(mode string) string {
	if mode == "" {
		return "direct"
	}
	return mode
}

// record bot reply for cooldown and rate limit
func noteBotReply(ev transportEvent) {
	if ev.ChatID >= 0 {
		return
	}
	now := time.Now()
	groupRuntimeMu.Lock()
	act := groupChats[ev.ChatID]
	if act == nil {
		act = &chatActivity{}
		groupChats[ev.ChatID] = act
	}
	groupChatsIdle.touch(ev.ChatID)
	act.lastReply = now
	act.replies = append(act.replies, now)
	groupRuntimeMu.Unlock()
}
