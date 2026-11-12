package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"spermacore/core/dashboard"
	scEmbedsc "spermacore/core/embedsc"
	"spermacore/core/humanify"
	"spermacore/core/media"
	"spermacore/core/webui"
)

const defaultWorkerCount = 4

func parseInt64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

var (
	toolsUnsupported atomic.Bool

	gDash *dashboard.Dashboard

	gHistory = NewChatHistory()

	taskQueue chan workerTask

	llmSem chan struct{}

	cacheMu       sync.RWMutex
	cacheKey      string
	cachedTools   []map[string]any
	cachedOwners  map[string]*LoadedAddon
	cachedPrompts string

	metricErrors   atomic.Int64
	metricQueueLen atomic.Int64
	metricActive   atomic.Int64

	// track delayed message part sends
	bgSendWG sync.WaitGroup
)

// interruptible sleep on shutdown
func sleepOrShutdown(d time.Duration, done <-chan struct{}) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-done:
		return false
	}
}

// embeddingLabel returns the text stored in semantic memory for an incoming
// event, falling back to a short media tag when the message has no text.
func embeddingLabel(ev transportEvent) string {
	if ev.IsCallback {
		return ""
	}
	if t := strings.TrimSpace(ev.Text); t != "" {
		return t
	}
	switch ev.MediaType {
	case "", "text":
		return ""
	case "photo":
		return "[photo]"
	case "gif", "video", "video_msg":
		return "[video]"
	case "sticker", "animated_sticker":
		return "[sticker]"
	case "voice":
		return "[voice]"
	case "audio":
		return "[audio]"
	default:
		return "[" + ev.MediaType + "]"
	}
}

// voiceChats marks chats that are currently delivering/synthesizing a voice
// note. While a chat is marked, the typing keep-alive must not re-send the
// "typing" action, otherwise Telegram flips the status back from
// "recording voice" to "typing" mid-synthesis.
var voiceChats sync.Map // chatID (int64) -> struct{}

func markVoiceActive(chatID int64)  { voiceChats.Store(chatID, struct{}{}) }
func clearVoiceActive(chatID int64) { voiceChats.Delete(chatID) }
func voiceActive(chatID int64) bool {
	_, ok := voiceChats.Load(chatID)
	return ok
}

// startTypingKeepAlive periodically re-sends the typing indicator while an
// answer is being generated, so the "typing..." state does not expire during
// long model calls. While a voice note is being produced the keep-alive stays
// silent (the voice path owns the chat action). The returned function stops
// the refresh.
func startTypingKeepAlive(ev transportEvent, topicID int) func() {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		SendTypingViaTransport(ev, topicID)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-shutdownCh:
				return
			case <-ticker.C:
				if voiceActive(ev.ChatID) {
					continue
				}
				SendTypingViaTransport(ev, topicID)
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}

// cancelTypingSoon clears a lingering typing/chat action after the final reply
// part has been delivered.
func cancelTypingSoon(ev transportEvent, topicID int) {
	SendTypingCancelViaTransport(ev, topicID)
}

// deliverVoiceReply synthesizes text and sends it as a voice note (ГС).
// The duration passed to the transport is read from the provider's audio.
func deliverVoiceReply(cfg *Config, ev transportEvent, text string, topicID int) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("voice: empty text")
	}

	// Own the chat action for the whole synthesis: switch to "recording voice"
	// immediately, keep it alive (it expires after a few seconds) and block the
	// typing keep-alive so the status never flips back to "typing".
	markVoiceActive(ev.ChatID)
	stopRecording := startVoiceActionKeepAlive(ev, topicID)
	defer func() {
		stopRecording()
		clearVoiceActive(ev.ChatID)
	}()

	speakable := ttsSpeakableText(cfg, text)
	path, seconds, err := synthesizeVoiceNote(cfg, speakable)
	if err != nil {
		return err
	}
	sendAsVoice := cfg == nil || cfg.TTS.ShouldSendAsVoice()
	var sendErr error
	if sendAsVoice {
		sendErr = sendVoiceViaTransport(ev, path, "", topicID, seconds)
	} else {
		sendErr = sendAudioViaTransport(ev, path, "", topicID, seconds)
	}
	if sendErr != nil {
		return fmt.Errorf("%w (audio file %q, %.1fs)", sendErr, path, seconds)
	}
	noteBotReply(ev)
	rememberOutgoingMessage(ev, text)
	return nil
}

// startVoiceActionKeepAlive switches the chat to "recording voice" and refreshes
// the action until the returned stop function is called. A cancel action is
// also sent on stop so the indicator does not linger after the voice note.
func startVoiceActionKeepAlive(ev transportEvent, topicID int) func() {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		SendRecordingVoiceViaTransport(ev, topicID)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				SendTypingCancelViaTransport(ev, topicID)
				return
			case <-shutdownCh:
				return
			case <-ticker.C:
				SendRecordingVoiceViaTransport(ev, topicID)
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}

// humanize delays interface
type humanizeDelays interface {
	Humanize(string) string
	DelayBefore(string) time.Duration
	DelayBetween() time.Duration
}

// send humanized parts sequentially
func sendHumanizedParts(parts []string, h humanizeDelays, done <-chan struct{},
	send func(index int, text string) error) bool {
	return sendHumanizedPartsWithHook(parts, h, done, nil, send)
}

func sendHumanizedPartsWithHook(parts []string, h humanizeDelays, done <-chan struct{},
	beforeWait func(index int),
	send func(index int, text string) error) bool {
	for i, part := range parts {
		out := h.Humanize(part)
		if i > 0 {
			// Realistic idle pause between messages before starting to type the next one
			idlePause := time.Duration(600+rand.Intn(700)) * time.Millisecond
			if !sleepOrShutdown(idlePause, done) {
				return false
			}
		}
		delay := h.DelayBetween()
		if i == 0 {
			delay = h.DelayBefore(out)
		}
		if beforeWait != nil {
			beforeWait(i)
		}
		if !sleepOrShutdown(delay, done) {
			return false
		}
		if err := send(i, out); err != nil {
			return false
		}
	}
	return true
}

func deliverWorkerError(ev transportEvent, err error, cfg *Config, log *Logger) {
	if cfg != nil && cfg.Core.ErrorDelivery == "user" {
		if sendErr := trySendReply("worker", ev, "error: "+err.Error(), log); sendErr != nil {
			log.Error("error sending error notification: %v", sendErr)
		}
		return
	}
	if w := webui.Global(); w != nil {
		w.Log("ERR", "worker", fmt.Sprintf("chat=%d: %v", ev.ChatID, err))
		return
	}
	log.Error("error processing chat %d: %v", ev.ChatID, err)
}

func worker(id int, log *Logger) {
	log.Debug("worker %d started", id)
	for {
		select {
		case <-shutdownCh:
			log.Debug("worker %d stopped", id)
			return
		default:
		}

		var task workerTask
		select {
		case <-shutdownCh:
			log.Debug("worker %d stopped", id)
			return
		case task = <-taskQueue:
		}

		select {
		case <-shutdownCh:
			tasksWG.Done()
			metricQueueLen.Add(-1)
			continue
		default:
		}

		metricQueueLen.Add(-1)
		metricActive.Add(1)

		taskTimeout := 10 * time.Minute
		if isPriorityUser(task.ev) {
			taskTimeout = 15 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), taskTimeout)

		if task.ev.MessageID > 0 {
			markReadWithDelay(task.cfg, task.ev.Transport, task.ev.ChatID, task.ev.MessageID, true)
		}

		photoPath := task.ev.PhotoPath
		task.ev.PhotoPath = photoPath

		if embText := embeddingLabel(task.ev); embText != "" && scEmbedsc.Enabled() {
			if err := scEmbedsc.Remember(task.ev.ChatID, "user", embText); err != nil {
				task.log.Warn("failed to remember in memory for chat %d: %v", task.ev.ChatID, err)
			}
		}
		// Catalog stickers for later reuse. Animated stickers cannot be sent to
		// the vision model directly, so prefer their static thumbnail; if none
		// is available fall back to the first extracted video frame.
		if task.ev.MediaType == "sticker" && photoPath != "" {
			descPath := photoPath
			if task.ev.StickerAnimated && task.ev.StickerThumbPath != "" {
				descPath = task.ev.StickerThumbPath
			}
			if desc := describeSticker(ctx, descPath, task.provider, task.cfg, task.log); desc != "" {
				stickerID := task.ev.StickerID
				if stickerID == "" {
					stickerID = fmt.Sprintf("%d:%d", task.ev.ChatID, task.ev.MessageID)
				}
				media.RememberSticker(task.cfg.Core.DataDir, media.StickerRecord{ID: stickerID, ChatID: task.ev.ChatID, MessageID: task.ev.MessageID, DocumentID: parseInt64(task.ev.StickerID), AccessHash: task.ev.StickerAccessHash, FileReference: task.ev.StickerFileRef, FileID: task.ev.StickerFileID, Emoji: task.ev.StickerEmoji, Description: desc, File: photoPath, UpdatedAt: time.Now()})
				task.log.Info("sticker cataloged: id=%s chat=%d animated=%t", stickerID, task.ev.ChatID, task.ev.StickerAnimated)
			} else if task.ev.StickerAnimated {
				task.log.Warn("animated sticker vision failed: id=%s chat=%d", task.ev.StickerID, task.ev.ChatID)
			}
		}
		if len(task.ev.CustomEmojiIDs) > 0 {
			for _, cid := range task.ev.CustomEmojiIDs {
				idStr := strconv.FormatInt(cid, 10)
				existing := media.FindEmoji(task.cfg.Core.DataDir, idStr)
				if existing == nil || strings.TrimSpace(existing.Description) == "" {
					go catalogCustomEmoji(context.Background(), cid, "", "", task.provider, task.cfg, task.log)
				}
			}
		}

		// streaming for webui
		var onToken func(string)
		var streamID string
		if w := webui.Global(); w != nil {
			streamID = w.StreamStart(task.ev.ChatID, task.ev.SenderName)
			onToken = func(tok string) {
				w.StreamToken(streamID, tok)
			}
		}

		// realistic "read and think" pause before the bot starts typing, so a
		// fresh incoming message does not trigger an instant typing indicator
		if h := humanify.Global(); h != nil && h.Enabled() {
			if !sleepOrShutdown(h.DelayBefore(task.ev.Text), shutdownCh) {
				metricActive.Add(-1)
				cancel()
				tasksWG.Done()
				continue
			}
		}

		// Voice transcription: transcribe incoming voice messages and audio files
		// so the model reads the recognized text instead of attaching raw audio.
		if task.ev.PhotoPath != "" && (task.ev.MediaType == "voice" || task.ev.MediaType == "audio") {
			if tr, trErr := transcribeAudioFile(task.cfg, task.ev.PhotoPath); trErr != nil {
				task.log.Warn("[worker %d] voice transcription failed: %v", id, trErr)
			} else if strings.TrimSpace(tr) != "" {
				task.log.Info("[worker %d] voice transcription: %.120q", id, tr)
				if strings.TrimSpace(task.ev.Text) != "" {
					task.ev.Text = strings.TrimSpace(task.ev.Text) + "\n" + strings.TrimSpace(tr)
				} else {
					task.ev.Text = strings.TrimSpace(tr)
				}
				task.ev.PhotoPath = ""
				task.ev.MediaType = ""
			}
		}

		// keep the typing indicator alive while the model generates; without
		// this the indicator silently expires during long model calls
		stopTyping := startTypingKeepAlive(task.ev, task.ev.TopicID)
		start := time.Now()

		gate := getChatGate(task.ev.ChatID)
		gate.RegisterInFlight(cancel)

		answer, err := reactLoop(ctx, task.ev, task.addons, task.provider, task.cfg, task.log, onToken)
		stopTyping()
		elapsed := time.Since(start)
		gate.ClearInFlight()
		cancel()

		if w := webui.Global(); w != nil && streamID != "" {
			w.StreamEnd(streamID, answer, err)
		}

		if err == nil && strings.TrimSpace(answer) != "" {
			if scEmbedsc.Enabled() {
				if rememberErr := scEmbedsc.Remember(task.ev.ChatID, "assistant", answer); rememberErr != nil {
					task.log.Warn("failed to remember assistant response in memory (chat %d): %v", task.ev.ChatID, rememberErr)
				}
			}
			reflectUserFactsBackground(task.ev, task.provider, task.cfg, task.log)
		}

		metricActive.Add(-1)

		if gDash != nil {
			gDash.TrackReply(elapsed)
		}
		if err != nil {
			if errors.Is(err, errIgnoreMessage) {
				task.log.Info("[worker %d] chat %d skipped by model", id, task.ev.ChatID)
				tasksWG.Done()
				continue
			}
			if errors.Is(err, errAlreadyDelivered) {
				task.log.Info("[worker %d] chat %d reply already sent via tool", id, task.ev.ChatID)
				tasksWG.Done()
				continue
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "context canceled") {
				task.log.Info("[worker %d] chat %d canceled (superseded or shutdown)", id, task.ev.ChatID)
				cancelTypingSoon(task.ev, task.ev.TopicID)
				tasksWG.Done()
				continue
			}
			task.log.Error("[worker %d] error processing chat %d: %v", id, task.ev.ChatID, err)
			deliverWorkerError(task.ev, err, task.cfg, task.log)
			tasksWG.Done()
			continue
		}
		if strings.TrimSpace(answer) == "" {
			task.log.Info("[worker %d] chat %d empty reply ignored", id, task.ev.ChatID)
			cancelTypingSoon(task.ev, task.ev.TopicID)
			tasksWG.Done()
			continue
		}

		// split AI bubbles
		parts, actions := splitAIResponse(answer)
		applyAIActions(task.cfg.Core.DataDir, task.ev, actions)
		if len(parts) == 1 {
			if h := humanify.Global(); h != nil && h.Enabled() {
				parts = h.Split(parts[0])
			}
		}

		topicID, _, topicActions := responseTopic(answer)
		applyAIActions(task.cfg.Core.DataDir, task.ev, topicActions)
		if topicID <= 0 {
			topicID = task.ev.TopicID
		}
		if before, after, ok, editActions := parseEditResponse(answer); ok {
			applyAIActions(task.cfg.Core.DataDir, task.ev, append(actions, editActions...))
			sentID, sendErr := sendTextGetID(task.ev, before, topicID)
			if sendErr == nil {
				noteBotReply(task.ev)
				rememberOutgoingMessage(task.ev, after)
				if editErr := editMessageViaTransport(task.ev, sentID, after); editErr != nil {
					task.log.Warn("failed to edit message: %v", editErr)
				}
			} else {
				task.log.Warn("failed to send initial message: %v", sendErr)
			}
			parts = nil
		}

		// |VOICE| asks for the reply as a voice note (ГС). The duration comes
		// from the synthesized audio itself, so the voice note header matches
		// what the provider actually returned.
		if voiceText, ok := voiceActionText(actions, parts); ok {
			if voiceErr := deliverVoiceReply(task.cfg, task.ev, voiceText, topicID); voiceErr != nil {
				task.log.Warn("[worker %d] voice reply failed, falling back to text: %v", id, voiceErr)
				if len(parts) == 0 {
					parts = []string{voiceText}
				}
			} else {
				parts = nil
			}
		}

		// Inside a call every answer is spoken, so the user hears the bot
		// instead of receiving text while the "call" is going on.
		if len(parts) > 0 && callSessionActive(task.ev.ChatID) {
			spoken := strings.TrimSpace(strings.Join(parts, "\n"))
			if voiceErr := deliverVoiceReply(task.cfg, task.ev, spoken, topicID); voiceErr != nil {
				task.log.Warn("[worker %d] call voice reply failed, falling back to text: %v", id, voiceErr)
			} else {
				parts = nil
			}
		}

		task.log.Info("[worker %d] chat %d processed in %.2fs", id, task.ev.ChatID, elapsed.Seconds())

		if len(parts) == 0 {
			tasksWG.Done()
			continue
		}

		h := humanify.Global()
		if h == nil || !h.Enabled() {
			for i, part := range parts {
				if err := trySendReplyModeTopic("worker", task.ev, part, task.log, i, topicID); err != nil {
					task.log.Error("[worker %d] send error: %v", id, err)
					break
				}
				if i == 0 {
					noteBotReply(task.ev)
				}
				rememberOutgoingMessage(task.ev, part)
			}
			cancelTypingSoon(task.ev, topicID)
			tasksWG.Done()
			continue
		}

		// humanized background send
		bgSendWG.Add(1)
		ev := task.ev
		go func(id int, ev transportEvent, parts []string, topicID int, log *Logger) {
			defer bgSendWG.Done()
			if len(parts) == 0 {
				return
			}
			// per-chat serialization
			gate := getChatGate(ev.ChatID)
			if !gate.AcquireWithDone(shutdownCh) {
				return
			}
			defer gate.Release()
			ok := sendHumanizedPartsWithHook(parts, h, shutdownCh, func(i int) {
				SendTypingViaTransport(ev, topicID)
			}, func(i int, out string) error {
				original := parts[i]
				hasTypo := (out != original)

				if h.AutoFixTypo() && hasTypo {
					sentID, err := sendTextGetID(ev, out, topicID)
					if err == nil {
						if i == 0 {
							noteBotReply(ev)
						}
						rememberOutgoingMessage(ev, out)

						// delayed typo correction
						go func(sentMsgID int, correctText string) {
							delay := time.Duration(1800+rand.Intn(1700)) * time.Millisecond
							t := time.NewTimer(delay)
							defer t.Stop()
							select {
							case <-t.C:
							case <-shutdownCh:
								return
							}
							if editErr := editMessageViaTransport(ev, sentMsgID, correctText); editErr != nil {
								log.Warn("failed to fix typo (message %d): %v", sentMsgID, editErr)
							} else {
								log.Info("fixed typo in message %d: %q -> %q", sentMsgID, out, correctText)
								rememberOutgoingMessage(ev, correctText)
							}
						}(sentID, original)
						return nil
					}
				}

				if err := trySendReplyModeTopic("worker", ev, out, log, i, topicID); err != nil {
					log.Error("[worker %d] send error part %d/%d: %v", id, i+1, len(parts), err)
					return err
				}
				if i == 0 {
					noteBotReply(ev)
				}
				rememberOutgoingMessage(ev, out)
				return nil
			})
			if ok && len(parts) > 1 {
				log.Debug("[worker %d] sent parts: %d", id, len(parts))
			}
			cancelTypingSoon(ev, topicID)
		}(id, ev, parts, topicID, task.log)
		tasksWG.Done()
	}
}
