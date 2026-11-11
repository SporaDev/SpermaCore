package main

import (
	"fmt"
	"strings"
)

// callbackEventText builds the user-facing text for an inline callback event so
// the model sees the button press as a normal incoming message.
func callbackEventText(ev transportEvent) string {
	var b strings.Builder
	b.WriteString("[inline callback] a user pressed ")
	if strings.TrimSpace(ev.ButtonText) != "" {
		b.WriteString("the button «" + strings.TrimSpace(ev.ButtonText) + "»")
	} else {
		b.WriteString("an inline button")
	}
	if payload := callbackPayloadPreview(ev); payload != "" {
		b.WriteString(" (data: " + payload + ")")
	}
	if text := strings.TrimSpace(ev.Text); text != "" {
		b.WriteString(" on the message: «" + truncate(text, 300) + "»")
	}
	return b.String()
}

func callbackPayloadPreview(ev transportEvent) string {
	if t := strings.TrimSpace(ev.CallbackDataText); t != "" {
		return truncate(t, 160)
	}
	if d := strings.TrimSpace(ev.CallbackData); d != "" {
		return truncate(d, 160)
	}
	return ""
}

// inlineEventContext renders tool guidance for inline bots, keyboards, callback
// queries and group invite links. It is appended to the system prompt.
func inlineEventContext(ev transportEvent) string {
	var sections []string

	if ev.ViaBotID != 0 || strings.TrimSpace(ev.ViaBotUsername) != "" || strings.TrimSpace(ev.ViaBotName) != "" {
		who := strings.TrimSpace(ev.ViaBotName)
		if u := strings.TrimPrefix(strings.TrimSpace(ev.ViaBotUsername), "@"); u != "" {
			if who != "" {
				who += " "
			}
			who += "(@" + u + ")"
		}
		if who == "" {
			who = fmt.Sprintf("id:%d", ev.ViaBotID)
		}
		sections = append(sections,
			"=== INLINE BOT ATTRIBUTION ===\n"+
				"this message was sent through the inline bot "+who+".\n"+
				"mention which bot produced it when that matters to the conversation.")
	}

	if ev.IsCallback {
		var b strings.Builder
		b.WriteString("=== INLINE CALLBACK ===\n")
		b.WriteString(fmt.Sprintf("callback_query_id = %d\n", ev.CallbackQueryID))
		if ev.ButtonText != "" {
			b.WriteString("button = " + ev.ButtonText + "\n")
		}
		if payload := callbackPayloadPreview(ev); payload != "" {
			b.WriteString("payload = " + payload + "\n")
		}
		b.WriteString("the user just pressed this inline button. react to the press; " +
			"you can send a normal reply with send_message and optionally answer the " +
			"callback with answer_callback(callback_query_id=..., text=\"...\") so the " +
			"spinner stops and the user sees a toast.")
		sections = append(sections, b.String())
	}

	if ev.Poll != nil {
		sections = append(sections, renderPoll(ev))
	}

	if len(ev.Buttons) > 0 {
		sections = append(sections,
			"=== INLINE KEYBOARD ON CURRENT MESSAGE ===\n"+
				renderButtons(ev.Buttons)+"\n"+
				fmt.Sprintf("press one with press_button(chat_id=%d, message_id=%d, button=\"<text or index>\"). ", ev.ChatID, ev.MessageID)+
				"call press_button only when pressing the button is actually useful; "+
				"url buttons only open a link and cannot be pressed.")
	}

	if link := findInviteLink(ev.Text); link != "" && !ev.IsCallback {
		sections = append(sections,
			"=== GROUP INVITE LINK ===\n"+
				"the message contains a Telegram invite link: "+link+"\n"+
				"if joining is relevant, first call check_invite(link) to inspect the chat "+
				"(title, member count, public or not), then decide whether to join. "+
				"Call join_chat(link) only when you actually want to join or the user asked you to. "+
				"Never join blindly.")
	}

	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

func renderPoll(ev transportEvent) string {
	p := ev.Poll
	var b strings.Builder
	b.WriteString("=== TELEGRAM POLL ON CURRENT MESSAGE ===\n")
	b.WriteString("question: " + truncate(p.Question, 500) + "\n")
	kind := "poll"
	if p.IsQuiz {
		kind = "quiz"
	}
	fmt.Fprintf(&b, "type = %s, total votes = %d, ", kind, p.TotalVoters)
	if p.IsAnonymous {
		b.WriteString("anonymous, ")
	} else {
		b.WriteString("public, ")
	}
	if p.AllowsMultipleAnswers {
		b.WriteString("multiple answers allowed, ")
	}
	if p.IsClosed {
		b.WriteString("CLOSED\n")
	} else {
		b.WriteString("open\n")
	}
	b.WriteString("options (0-based ids for vote_poll):\n")
	for _, o := range p.Options {
		fmt.Fprintf(&b, "  id=%d. %s", o.Index, truncate(o.Text, 200))
		if o.VoterCount > 0 {
			fmt.Fprintf(&b, " (%d votes)", o.VoterCount)
		}
		if o.IsChosen {
			b.WriteString(" [already chosen by you]")
		}
		b.WriteString("\n")
	}
	if p.IsClosed {
		b.WriteString("the poll is closed: do not try to vote.")
	} else {
		fmt.Fprintf(&b, "vote with vote_poll(chat_id=%d, message_id=%d, option_ids=[...]) ", ev.ChatID, ev.MessageID)
		if p.IsQuiz || !p.AllowsMultipleAnswers {
			b.WriteString("using exactly one option id, ")
		}
		b.WriteString("only when voting actually fits the conversation; do not vote on every poll you see.")
	}
	return b.String()
}

func renderButtons(buttons []InlineButton) string {
	var b strings.Builder
	for _, btn := range buttons {
		idx := btn.Index
		if idx == 0 {
			idx = 1
		}
		fmt.Fprintf(&b, "%d. %q [%s]", idx, btn.Text, btn.Type)
		if btn.URL != "" {
			b.WriteString(" url=" + btn.URL)
		}
		if payload := strings.TrimSpace(btn.DataText); payload != "" {
			b.WriteString(" data=" + truncate(payload, 120))
		} else if btn.Data != "" {
			b.WriteString(" data=" + truncate(btn.Data, 80))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// findInviteLink returns the first Telegram invite link found in text, if any.
func findInviteLink(text string) string {
	for _, field := range strings.Fields(text) {
		token := strings.Trim(field, "()[]{}<>.,;:!?\"'«»")
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		if strings.Contains(lower, "t.me/+") ||
			strings.Contains(lower, "t.me/joinchat/") ||
			strings.Contains(lower, "telegram.me/+") ||
			strings.Contains(lower, "telegram.me/joinchat/") ||
			strings.Contains(lower, "tg://join?invite=") {
			return token
		}
	}
	return ""
}
