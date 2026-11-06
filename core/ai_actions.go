package main

import (
	"regexp"
	"strconv"
	"strings"
)

var aiActionPattern = regexp.MustCompile(`\|([A-Z]+)(?:=([^|]+))?\|`)

type aiAction struct{ Name, Value string }

func responseTopic(text string) (int, string, []aiAction) {
	clean, actions := parseAIActions(text)
	for _, a := range actions {
		if a.Name == "TOPIC" {
			if n, err := strconv.Atoi(a.Value); err == nil && n > 0 {
				return n, clean, actions
			}
		}
	}
	return 0, clean, actions
}

func parseAIActions(text string) (string, []aiAction) {
	var actions []aiAction
	clean := aiActionPattern.ReplaceAllStringFunc(text, func(token string) string {
		m := aiActionPattern.FindStringSubmatch(token)
		actions = append(actions, aiAction{Name: m[1], Value: strings.TrimSpace(m[2])})
		return ""
	})
	return strings.TrimSpace(clean), actions
}

func splitAIResponse(text string) (parts []string, actions []aiAction) {
	for _, raw := range strings.Split(text, "|SPLIT|") {
		clean, found := parseAIActions(raw)
		actions = append(actions, found...)
		for _, part := range strings.Split(clean, "|NEXT|") {
			if part = strings.TrimSpace(part); part != "" {
				parts = append(parts, part)
			}
		}
	}
	return parts, actions
}

func parseEditResponse(text string) (before, after string, ok bool, actions []aiAction) {
	parts := strings.SplitN(text, "|EDIT|", 2)
	if len(parts) != 2 {
		return "", "", false, nil
	}
	before, actions = parseAIActions(strings.TrimSpace(parts[0]))
	after, found := parseAIActions(strings.TrimSpace(parts[1]))
	actions = append(actions, found...)
	return before, after, before != "" && after != "", actions
}

func applyAIActions(dataDir string, ev transportEvent, actions []aiAction) {
	if dataDir == "" {
		if gCfg != nil && gCfg.Core.DataDir != "" {
			dataDir = gCfg.Core.DataDir
		} else {
			dataDir = "./data"
		}
	}
	for _, action := range actions {
		if action.Name == "IMPORTANT" {
			handleImportantAction(dataDir, ev, action.Value)
			continue
		}
		if action.Name == "IGNORE" {
			setUserIgnored(ev.SenderUsername, true)
			continue
		}
		if action.Name == "UNIGNORE" {
			setUserIgnored(ev.SenderUsername, false)
			continue
		}
		if action.Name == "PHOTO" || action.Name == "GIF" {
			val := action.Value
			caption := ""
			path := val
			if idx := strings.Index(val, "::"); idx != -1 {
				path = strings.TrimSpace(val[:idx])
				caption = strings.TrimSpace(val[idx+2:])
			}
			if path != "" && ev.Transport == "telegram" {
				SendTypingViaTransport(ev, ev.TopicID)
				_ = sendPhotoViaTransport(ev, path, caption, ev.TopicID)
			}
			continue
		}
		if action.Name == "REACT" {
			owner := transportOwner(ev.Transport)
			if owner != nil && owner.HasNativeTransport() {
				fields := map[string]any{
					"chat_id":    strconv.FormatInt(ev.ChatID, 10),
					"message_id": ev.MessageID,
					"emoji":      action.Value,
					"transport":  ev.Transport,
				}
				_, _ = owner.TransportReact(transportPayload(fields))
			}
			continue
		}
		if action.Name != "SETPRIORITY" {
			continue
		}
		level, err := strconv.Atoi(action.Value)
		if err != nil || level < 0 || level > 3 {
			continue
		}
		setUserPriority(ev.SenderUsername, level)
	}
}
