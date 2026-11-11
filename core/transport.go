package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	scEmbedsc "spermacore/core/embedsc"
)

// transport capabilities matching plugin_api.h
const (
	transportCapText        uint32 = 1 << 0
	transportCapMedia       uint32 = 1 << 1
	transportCapReactions   uint32 = 1 << 2
	transportCapTyping      uint32 = 1 << 3
	transportCapEdit        uint32 = 1 << 4
	transportCapDelete      uint32 = 1 << 5
	transportCapReadReceipt uint32 = 1 << 6
	transportCapThreads     uint32 = 1 << 7
	transportCapVoice       uint32 = 1 << 8
)

// sc_transport_info_t method indices
const (
	transportCallText   = 0
	transportCallMedia  = 1
	transportCallReact  = 2
	transportCallRead   = 3
	transportCallTyping = 4
	transportCallEdit   = 5
	transportCallDelete = 6
	transportCallPoll   = 7
)

type workerTask struct {
	ev       transportEvent
	addons   []*LoadedAddon
	provider LLMProvider
	cfg      *Config
	log      *Logger
}

var gIngestSink func(string)

func setIngestSink(f func(string)) {
	gIngestSink = f
}

// ingest event entrypoint
func ingestEvent(eventJSON string) int {
	if gIngestSink == nil {
		return -1
	}
	gIngestSink(eventJSON)
	return 0
}

// InlineButton describes one button of an inline keyboard attached to a
// message. Data carries the raw base64 callback payload TDLib uses.
type InlineButton struct {
	Index    int    `json:"index,omitempty"`
	Row      int    `json:"row,omitempty"`
	Col      int    `json:"col,omitempty"`
	Text     string `json:"text"`
	Type     string `json:"type,omitempty"`
	Data     string `json:"data,omitempty"`
	DataText string `json:"data_text,omitempty"`
	URL      string `json:"url,omitempty"`
}

// PollOption is one answer option of a Telegram poll. Index is the 0-based
// option id used by the vote_poll tool.
type PollOption struct {
	Index      int    `json:"index"`
	Text       string `json:"text"`
	VoterCount int    `json:"voter_count,omitempty"`
	IsChosen   bool   `json:"is_chosen,omitempty"`
}

// Poll describes a Telegram poll or quiz attached to an incoming message.
type Poll struct {
	Question              string       `json:"question"`
	Options               []PollOption `json:"options"`
	TotalVoters           int          `json:"total_voter_count,omitempty"`
	IsAnonymous           bool         `json:"is_anonymous,omitempty"`
	AllowsMultipleAnswers bool         `json:"allows_multiple_answers,omitempty"`
	IsQuiz                bool         `json:"is_quiz,omitempty"`
	IsClosed              bool         `json:"is_closed,omitempty"`
}

// incoming transport event
type transportEvent struct {
	Type           string `json:"type"`
	Transport      string `json:"transport,omitempty"`
	ChatID         int64  `json:"chat_id"`
	SenderID       int64  `json:"sender_id"`
	SenderName     string `json:"sender_name,omitempty"`
	SenderUsername string `json:"sender_username,omitempty"`
	MessageID      int    `json:"message_id,omitempty"`
	ReplyToID      int    `json:"reply_to_id,omitempty"`
	ReplyFromSelf  bool   `json:"reply_from_self,omitempty"`
	SelfUsername   string `json:"self_username,omitempty"`
	SelfName       string `json:"self_name,omitempty"`
	SelfID         int64  `json:"self_id,omitempty"`
	TopicID        int    `json:"topic_id,omitempty"`
	Forum          bool   `json:"forum,omitempty"`
	Text           string `json:"text"`
	MediaType      string `json:"media_type,omitempty"`
	IsEdit         bool   `json:"is_edit,omitempty"`
	Date           int64  `json:"date,omitempty"`

	PhotoPath         string  `json:"photo_path,omitempty"`
	CustomEmojiIDs    []int64 `json:"custom_emoji_ids,omitempty"`
	StickerID         string  `json:"sticker_id,omitempty"`
	StickerEmoji      string  `json:"sticker_emoji,omitempty"`
	StickerAccessHash int64   `json:"sticker_access_hash,omitempty"`
	StickerFileRef    []byte  `json:"sticker_file_reference,omitempty"`
	StickerFileID     int     `json:"sticker_file_id,omitempty"`
	StickerAnimated   bool    `json:"sticker_animated,omitempty"`
	StickerThumbPath  string  `json:"sticker_thumb_path,omitempty"`
	StickerWidth      int     `json:"sticker_width,omitempty"`
	StickerHeight     int     `json:"sticker_height,omitempty"`

	// Inline bot attribution and inline keyboards.
	ViaBotID       int64          `json:"via_bot_id,omitempty"`
	ViaBotName     string         `json:"via_bot_name,omitempty"`
	ViaBotUsername string         `json:"via_bot_username,omitempty"`
	Buttons        []InlineButton `json:"buttons,omitempty"`

	// Poll/quiz attached to the message (media_type == "poll").
	Poll *Poll `json:"poll,omitempty"`

	// Callback query (a user pressed an inline button on one of our messages).
	CallbackQueryID  int64  `json:"callback_query_id,omitempty"`
	CallbackData     string `json:"callback_data,omitempty"`
	CallbackDataText string `json:"callback_data_text,omitempty"`
	ButtonText       string `json:"button_text,omitempty"`
	IsCallback       bool   `json:"-"`

	SemanticContext string `json:"-"`
	InlineContext   string `json:"-"`
	Activation      string `json:"-"`
}

// normalizeIngestEvent classifies an incoming plugin event and attaches the
// inline context (bot attribution, keyboards, callback presses, invite links).
// It reports false for events the core does not handle.
func normalizeIngestEvent(ev transportEvent) (transportEvent, bool) {
	switch ev.Type {
	case "new_message", "edit_message":
		if ev.Type == "edit_message" {
			ev.IsEdit = true
		}
	case "callback_query":
		ev.IsCallback = true
		ev.Text = callbackEventText(ev)
	default:
		return ev, false
	}
	ev.InlineContext = inlineEventContext(ev)
	return ev, true
}

// transport sink factory
func makeTransportSink(name string, addons []*LoadedAddon, provider LLMProvider,
	cfg *Config, log *Logger) func(string) {

	submit := makeSubmitFunc(name, addons, provider, cfg, log)
	gr := GlobalGrouper()

	return func(eventJSON string) {
		var raw transportEvent
		if err := json.Unmarshal([]byte(eventJSON), &raw); err != nil {
			log.Error("[%s] bad event json: %v", name, err)
			return
		}
		ev, ok := normalizeIngestEvent(raw)
		if !ok {
			return
		}
		if ev.SelfUsername != "" {
			RegisterBotUsername(ev.SelfUsername)
		}
		if ev.SelfName != "" {
			RegisterBotName(ev.SelfName)
		}
		if isIgnoredUser(ev) {
			log.Debug("[%s] ignored blocked user=%s id=%d chat=%d", name, ev.SenderUsername, ev.SenderID, ev.ChatID)
			return
		}
		if strings.TrimSpace(ev.Text) == "" && ev.MediaType == "" {
			return
		}
		if dispatchHook(scHookBeforeEvent, eventJSON) {
			log.Debug("[%s] event suppressed by before_event (chat=%d)", name, ev.ChatID)
			return
		}

		if !ev.IsCallback {
			rememberIncomingMessage(ev)
			extractFastUserFacts(cfg.Core.DataDir, ev)
		}

		if gDash != nil {
			gDash.TrackMessage()
		}

		// enrich with semantic context
		if !ev.IsCallback && scEmbedsc.Enabled() && strings.TrimSpace(ev.Text) != "" {
			if hits, err := scEmbedsc.Recall(ev.ChatID, ev.Text, 5); err == nil && len(hits) > 0 {
				var contextLines []string
				for _, hit := range hits {
					if !hit.Record.TS.IsZero() && time.Since(hit.Record.TS) > 30*24*time.Hour {
						continue
					}
					if hit.Similarity >= 0.45 {
						contextLines = append(contextLines, fmt.Sprintf("[%s %.2f] %s", hit.Record.Role, hit.Similarity, truncate(hit.Record.Text, 240)))
					}
				}
				ev.SemanticContext = strings.Join(contextLines, "\n")
			} else if err != nil {
				log.Debug("[%s] semantic recall failed chat=%d: %v", name, ev.ChatID, err)
			}
		}

		// broadcast media to event bus
		if ev.MediaType != "" {
			emitMediaToPlugins(ev)
		}

		// check group policy
		if ev.ChatID < 0 && !ev.IsCallback {
			respond, mode := groupShouldRespond(ev)
			ev.Activation = mode
			if !respond {
				if scEmbedsc.Enabled() && strings.TrimSpace(ev.Text) != "" {
					if err := scEmbedsc.Remember(ev.ChatID, "user", ev.Text); err != nil {
						log.Debug("[%s] read-only group message not remembered: %v", name, err)
					}
				}
				log.Debug("[%s] group read-only chat=%d from=%s reason=%s", name, ev.ChatID, ev.SenderUsername, mode)
				markReadLater(ev, cfg)
				return
			}
			log.Debug("[%s] group respond chat=%d from=%s activation=%s", name, ev.ChatID, ev.SenderUsername, mode)
		}

		// grouper debounce (callback presses are handled immediately)
		if gr != nil && gr.Enabled() && !ev.IsCallback {
			gr.Add(ev)
			return
		}

		submit(ev)
	}
}

// submit worker task
func makeSubmitFunc(name string, addons []*LoadedAddon, provider LLMProvider,
	cfg *Config, log *Logger) func(transportEvent) {

	return func(ev transportEvent) {
		metricQueueLen.Add(1)
		log.Info("[%s] queue chat=%d from=%s media=%q: %.60q",
			name, ev.ChatID, ev.SenderUsername, ev.MediaType,
			truncate(ev.Text, 60))

		if !submitTask(workerTask{
			ev: ev, addons: addons, provider: provider, cfg: cfg, log: log,
		}) {
			metricQueueLen.Add(-1)
			log.Warn("[%s] dropping chat=%d (shutting down or queue full)", name, ev.ChatID)
		}
	}
}

// delayed mark read
func markReadLater(ev transportEvent, cfg *Config) {
	if ev.MessageID <= 0 {
		return
	}
	markReadWithDelay(cfg, ev.Transport, ev.ChatID, ev.MessageID, false)
}

// encode payload for transport info
func transportPayload(fields map[string]any) string {
	b, err := json.Marshal(fields)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// split by transport limit
func splitForTransport(text string, maxLen int) []string {
	if maxLen <= 0 || len([]rune(text)) <= maxLen {
		return []string{text}
	}
	runes := []rune(text)
	var out []string
	for len(runes) > maxLen {
		out = append(out, string(runes[:maxLen]))
		runes = runes[maxLen:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}

// send text via plugin transport
func sendViaPluginTransport(owner *LoadedAddon, ev transportEvent, text string, topicID int, partOffset int, log *Logger) error {
	transport := ev.Transport
	if transport == "" {
		transport = owner.TransportName
	}
	base := map[string]any{
		"chat_id":   strconv.FormatInt(ev.ChatID, 10),
		"transport": transport,
	}
	if ev.MessageID > 0 {
		base["reply_to"] = strconv.Itoa(ev.MessageID)
	}
	if topicID > 0 {
		base["topic_id"] = topicID
	}

	if owner.TransportSupports(transportCallTyping) && owner.TransportCapabilities()&transportCapTyping != 0 {
		go func() {
			_, _ = owner.TransportTyping(transportPayload(base))
		}()
	}

	var firstErr error
	for i, part := range splitForTransport(text, owner.TransportMaxTextLen()) {
		fields := make(map[string]any, len(base)+1)
		for k, v := range base {
			fields[k] = v
		}
		fields["part_index"] = partOffset + i
		fields["text"] = part
		resp, err := owner.TransportSendText(transportPayload(fields))
		if err != nil {
			log.Error("[%s] transport %s send: %v", owner.Name, transport, err)
			firstErr = err
			continue
		}
		if resp != "" {
			if log != nil {
				log.Debug("[%s] transport %s response: %s", owner.Name, transport, truncate(resp, 200))
			}
			var parsed struct {
				MessageID any `json:"message_id"`
			}
			if json.Unmarshal([]byte(resp), &parsed) == nil && parsed.MessageID != nil {
				var msgID int
				switch v := parsed.MessageID.(type) {
				case float64:
					msgID = int(v)
				case string:
					if id64, err := strconv.ParseInt(v, 10, 64); err == nil {
						msgID = int(id64)
					}
				}
				if msgID > 0 {
					rememberBotSentMessage(ev.ChatID, msgID)
				}
			}
		}
	}
	return firstErr
}

// send a raw chat action to the transport
func sendChatActionViaTransport(ev transportEvent, topicID int, action string) {
	owner := transportOwner(ev.Transport)
	if owner == nil || !owner.TransportSupports(transportCallTyping) || owner.TransportCapabilities()&transportCapTyping == 0 {
		return
	}
	base := map[string]any{
		"chat_id":   strconv.FormatInt(ev.ChatID, 10),
		"transport": ev.Transport,
		"action":    action,
	}
	if topicID > 0 {
		base["topic_id"] = topicID
	}
	go func() {
		_, _ = owner.TransportTyping(transportPayload(base))
	}()
}

// send typing indicator
func SendTypingViaTransport(ev transportEvent, topicID int) {
	sendChatActionViaTransport(ev, topicID, "typing")
}

// cancel the current typing indicator so it does not linger after the reply
func SendTypingCancelViaTransport(ev transportEvent, topicID int) {
	sendChatActionViaTransport(ev, topicID, "cancel")
}

// send recording voice indicator
func SendRecordingVoiceViaTransport(ev transportEvent, topicID int) {
	owner := transportOwner(ev.Transport)
	if owner == nil || !owner.TransportSupports(transportCallTyping) {
		return
	}
	base := map[string]any{
		"chat_id":   strconv.FormatInt(ev.ChatID, 10),
		"transport": ev.Transport,
		"action":    "recording_voice",
	}
	if topicID > 0 {
		base["topic_id"] = topicID
	}
	go func() {
		_, _ = owner.TransportTyping(transportPayload(base))
	}()
}

// send reply to originating transport
func trySendReply(name string, ev transportEvent, text string, log *Logger) error {
	return trySendReplyTopic(name, ev, text, log, 0, ev.TopicID)
}

func trySendReplyTopic(name string, ev transportEvent, text string, log *Logger, partOffset int, topicID int) error {
	return trySendReplyModeTopic(name, ev, text, log, partOffset, topicID)
}

func trySendReplyModeTopic(name string, ev transportEvent, text string, log *Logger, partOffset int, topicID int) error {
	transport := ev.Transport
	if transport == "" {
		transport = name
	}

	payload := sendHookPayload(transport, ev.ChatID, text)
	if dispatchHook(scHookBeforeSend, payload) {
		log.Debug("[%s] send suppressed by before_send (chat=%d)", name, ev.ChatID)
		return nil
	}

	owner := transportOwner(transport)
	if owner != nil {
		if owner.HasNativeTransport() && owner.TransportSupports(transportCallText) {
			err := sendViaPluginTransport(owner, ev, text, topicID, partOffset, log)
			dispatchHook(scHookOnMessageSent, payload)
			return err
		}
		dispatchHookTo(owner, scHookOnTransportSend, payload)
		dispatchHook(scHookOnMessageSent, payload)
		return nil
	}

	return fmt.Errorf("unknown transport: %s", transport)
}
