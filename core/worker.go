package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
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

		embText := task.ev.Text
		if embText == "" && photoPath != "" {
			embText = "[photo]"
		}
		if scEmbedsc.Enabled() && embText != "" {
			if err := scEmbedsc.Remember(task.ev.ChatID, "user", embText); err != nil {
				task.log.Warn("failed to remember in memory for chat %d: %v", task.ev.ChatID, err)
			}
		}
		var tempFramesDir string
		if (task.ev.MediaType == "gif" || task.ev.MediaType == "video") && photoPath != "" {
			if frames, err := media.AnalyzeVideoFrames(ctx, photoPath); err == nil && len(frames) > 0 {
				photoPath = frames[0]
				task.ev.PhotoPath = frames[0]
				tempFramesDir = filepath.Dir(frames[0])
				task.log.Info("video analyzed: frames=%d, source=%s", len(frames), task.ev.MediaType)
			} else if err != nil {
				task.log.Warn("failed to extract video frames: %v", err)
			}
		}
		if task.ev.MediaType == "sticker" && photoPath != "" {
			if desc := describeSticker(ctx, photoPath, task.provider, task.cfg, task.log); desc != "" {
				stickerID := task.ev.StickerID
				if stickerID == "" {
					stickerID = fmt.Sprintf("%d:%d", task.ev.ChatID, task.ev.MessageID)
				}
				media.RememberSticker(task.cfg.Core.DataDir, media.StickerRecord{ID: stickerID, ChatID: task.ev.ChatID, MessageID: task.ev.MessageID, DocumentID: parseInt64(task.ev.StickerID), AccessHash: task.ev.StickerAccessHash, FileReference: task.ev.StickerFileRef, FileID: task.ev.StickerFileID, Emoji: task.ev.StickerEmoji, Description: desc, File: photoPath, UpdatedAt: time.Now()})
				task.log.Info("sticker cataloged: id=%s chat=%d", stickerID, task.ev.ChatID)
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

		SendTypingViaTransport(task.ev, task.ev.TopicID)
		start := time.Now()

		gate := getChatGate(task.ev.ChatID)
		if task.ev.ChatID > 0 {
			if gate.CancelInFlight() {
				task.log.Info("[worker %d] canceled previous in-flight task for chat %d due to new user message", id, task.ev.ChatID)
			}
		}
		gate.RegisterInFlight(cancel)

		answer, err := reactLoop(ctx, task.ev, task.addons, task.provider, task.cfg, task.log, onToken)
		elapsed := time.Since(start)
		gate.ClearInFlight()
		cancel()
		if tempFramesDir != "" {
			_ = os.RemoveAll(tempFramesDir)
		}

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
			if errors.Is(err, context.Canceled) {
				task.log.Info("[worker %d] chat %d canceled (superseded or shutdown)", id, task.ev.ChatID)
				tasksWG.Done()
				continue
			}
			task.log.Error("[worker %d] error processing chat %d: %v", id, task.ev.ChatID, err)
			deliverWorkerError(task.ev, err, task.cfg, task.log)
			tasksWG.Done()
			continue
		}
		if strings.TrimSpace(answer) == "" {
			answer = "(empty)"
		}

		// split AI bubbles
		parts, actions := splitAIResponse(answer)
		applyAIActions(task.cfg.Core.DataDir, task.ev, actions)
		if len(parts) == 0 {
			parts = []string{"(empty)"}
		}
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
		}(id, ev, parts, topicID, task.log)
		tasksWG.Done()
	}
}
