package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// actionAliases maps known action names and their common Russian / typo variants
// to the canonical uppercase action name.
var actionAliases = map[string]string{
	"BLOCK":       "BLOCK",
	"DELETECHAT":  "DELETECHAT",
	"EDIT":        "EDIT",
	"GIF":         "GIF",
	"IGNORE":      "IGNORE",
	"IMPORTANT":   "IMPORTANT",
	"NEXT":        "NEXT",
	"PHOTO":       "PHOTO",
	"REACT":       "REACT",
	"SETPRIORITY": "SETPRIORITY",
	"SPLIT":       "SPLIT",
	"TOPIC":       "TOPIC",
	"UNBLOCK":     "UNBLOCK",
	"UNIGNORE":    "UNIGNORE",
	"VOICE":       "VOICE",

	// Russian and common aliases so models replying in Russian don't leak markers
	"ВАЖНО":      "IMPORTANT",
	"СПЛИТ":      "SPLIT",
	"ГОЛОС":      "VOICE",
	"ГС":         "VOICE",
	"БЛОК":       "BLOCK",
	"РАЗБЛОК":    "UNBLOCK",
	"УДАЛИТЬЧАТ": "DELETECHAT",
	"РЕАКЦИЯ":    "REACT",
	"ФОТО":       "PHOTO",
	"ГИФ":        "GIF",
}

// aiActionPattern matches control markers with pipes or brackets, tolerating:
//   - markdown wrappers: **|SPLIT|**, *|VOICE|*
//   - separators: =, :, -, or whitespace (|IMPORTANT: exam|, |IMPORTANT=exam|, |IMPORTANT exam|)
//   - brackets: [IMPORTANT=exam], [SPLIT]
//   - missing closing pipe at end of line/string
var aiActionPattern = regexp.MustCompile(`(?i)(?:[*_\x60~]*\|\s*([A-Za-zА-Яа-яЁё]+)\s*(?:[=:\-\s]\s*([^|\]\r\n]*?)\s*)?(?:\|[*_\x60~]*|\r?\n|$)|[*_\x60~]*\[\s*([A-Za-zА-Яа-яЁё]+)\s*(?:[=:\-\s]\s*([^\]|\r\n]*?)\s*)?\][*_\x60~]*)`)

var (
	aiSplitPattern = regexp.MustCompile(`(?i)(?:[*_\x60~]*\s*\|\s*(?:SPLIT|СПЛИТ)\s*(?:\||\r?\n|$)[*_\x60~]*|\[\s*(?:SPLIT|СПЛИТ)\s*\]|\(\s*(?:SPLIT|СПЛИТ)\s*\))`)
	aiNextPattern  = regexp.MustCompile(`(?i)(?:[*_\x60~]*\s*\|\s*(?:NEXT|СЛЕДУЮЩЕЕ)\s*(?:\||\r?\n|$)[*_\x60~]*|\[\s*(?:NEXT|СЛЕДУЮЩЕЕ)\s*\])`)
	aiEditPattern  = regexp.MustCompile(`(?i)(?:[*_\x60~]*\s*\|\s*(?:EDIT|ПРАВКА)\s*(?:\||\r?\n|$)[*_\x60~]*|\[\s*(?:EDIT|ПРАВКА)\s*\])`)
)

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
		rawName := m[1]
		val := m[2]
		if rawName == "" {
			rawName = m[3]
			val = m[4]
		}
		name := strings.ToUpper(strings.TrimSpace(rawName))
		canonical, ok := actionAliases[name]
		if !ok {
			return token
		}
		val = strings.Trim(strings.TrimSpace(val), `"'«»`)
		actions = append(actions, aiAction{Name: canonical, Value: val})
		return ""
	})
	return strings.TrimSpace(clean), actions
}

func cleanPunctuationSpacing(text string) string {
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	text = regexp.MustCompile(`\s+([.,!?:;])`).ReplaceAllString(text, "$1")
	return text
}

var reLeadingTimestamp = regexp.MustCompile(`^(?:\[\d{1,2}:\d{2}(?::\d{2})?(?:\s*[^\]]+)?\]|\d{1,2}:\d{2}(?::\d{2})?\s*[-:]|\(\d{1,2}:\d{2}(?::\d{2})?\))\s*`)

func cleanPart(part string) string {
	part = strings.TrimSpace(part)
	part = strings.Trim(part, "| \t\r\n`*")
	part = reLeadingTimestamp.ReplaceAllString(part, "")
	return cleanPunctuationSpacing(part)
}

func splitAIResponse(text string) (parts []string, actions []aiAction) {
	for _, raw := range aiSplitPattern.Split(text, -1) {
		for _, chunk := range aiNextPattern.Split(raw, -1) {
			clean, found := parseAIActions(chunk)
			actions = append(actions, found...)
			if part := cleanPart(clean); part != "" {
				parts = append(parts, part)
			}
		}
	}
	return parts, actions
}

func parseEditResponse(text string) (before, after string, ok bool, actions []aiAction) {
	parts := aiEditPattern.Split(text, 2)
	if len(parts) != 2 {
		return "", "", false, nil
	}
	before, actions = parseAIActions(strings.TrimSpace(parts[0]))
	after, found := parseAIActions(strings.TrimSpace(parts[1]))
	actions = append(actions, found...)
	return before, after, before != "" && after != "", actions
}

// voiceActionText reports whether the reply asked for a voice note. A bare
// |VOICE| marker speaks the surrounding reply; |VOICE=text| speaks the given
// text. The boolean is false when the reply must be sent as text.
func voiceActionText(actions []aiAction, parts []string) (string, bool) {
	for _, a := range actions {
		if a.Name != "VOICE" {
			continue
		}
		if v := strings.TrimSpace(a.Value); v != "" {
			return v, true
		}
		joined := strings.TrimSpace(strings.Join(parts, "\n"))
		if joined == "" {
			return "", false
		}
		return joined, true
	}
	return "", false
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
		if action.Name == "IGNORE" || action.Name == "BLOCK" {
			target := action.Value
			if target == "" {
				target = ev.SenderUsername
			}
			if target == "" && ev.SenderID != 0 {
				target = fmt.Sprintf("id:%d", ev.SenderID)
			}
			if target != "" {
				setUserIgnored(target, true)
				if ev.SenderID != 0 {
					setUserIgnored(fmt.Sprintf("id:%d", ev.SenderID), true)
				}
			}
			if action.Name == "BLOCK" {
				_, _ = pluginSyscallJSON("tg.block_user", map[string]any{
					"username": target,
					"user_id":  ev.SenderID,
					"chat_id":  ev.ChatID,
				})
			}
			continue
		}
		if action.Name == "UNIGNORE" || action.Name == "UNBLOCK" {
			target := action.Value
			if target == "" {
				target = ev.SenderUsername
			}
			if target != "" {
				setUserIgnored(target, false)
				if ev.SenderID != 0 {
					setUserIgnored(fmt.Sprintf("id:%d", ev.SenderID), false)
				}
			}
			if action.Name == "UNBLOCK" {
				_, _ = pluginSyscallJSON("tg.unblock_user", map[string]any{
					"username": target,
					"user_id":  ev.SenderID,
					"chat_id":  ev.ChatID,
				})
			}
			continue
		}
		if action.Name == "DELETECHAT" {
			cid := ev.ChatID
			if action.Value != "" {
				if parsed, err := strconv.ParseInt(action.Value, 10, 64); err == nil && parsed != 0 {
					cid = parsed
				}
			}
			_, _ = pluginSyscallJSON("tg.delete_chat", map[string]any{
				"chat_id": cid,
			})
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
