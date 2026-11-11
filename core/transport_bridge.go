package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"
)

// resolveTransportOwner finds the transport addon for the given name or falls back to any active native transport
func resolveTransportOwner(transport string) *LoadedAddon {
	if transport != "" {
		if owner := transportOwner(transport); owner != nil {
			return owner
		}
	}
	busMu.RLock()
	defer busMu.RUnlock()
	for _, a := range busAddons {
		if a.HasNativeTransport() {
			return a
		}
	}
	return nil
}

func transportReady(transport string) bool {
	owner := resolveTransportOwner(transport)
	return owner != nil && owner.HasNativeTransport()
}

func makeTransportPayload(ev transportEvent, topicID int) map[string]any {
	transport := ev.Transport
	if transport == "" {
		transport = "generic"
	}
	fields := map[string]any{
		"chat_id":   strconv.FormatInt(ev.ChatID, 10),
		"transport": transport,
	}
	if ev.MessageID > 0 {
		fields["reply_to"] = strconv.Itoa(ev.MessageID)
	}
	if topicID > 0 {
		fields["topic_id"] = topicID
	}
	return fields
}

// send text and return message id
func sendTextGetID(ev transportEvent, text string, topicID int) (int, error) {
	owner := resolveTransportOwner(ev.Transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallText) {
		return 0, fmt.Errorf("transport %q unavailable", ev.Transport)
	}
	fields := makeTransportPayload(ev, topicID)
	fields["text"] = text
	resp, err := owner.TransportSendText(transportPayload(fields))
	if err != nil {
		return 0, err
	}
	var parsed struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal([]byte(resp), &parsed) != nil || parsed.MessageID == "" {
		return 0, fmt.Errorf("transport returned no message_id")
	}
	id64, convErr := strconv.ParseInt(parsed.MessageID, 10, 64)
	if convErr != nil {
		return 0, fmt.Errorf("invalid message_id %q: %w", parsed.MessageID, convErr)
	}
	id := int(id64)
	rememberBotSentMessage(ev.ChatID, id)
	return id, nil
}

// edit sent message
func editMessageViaTransport(ev transportEvent, messageID int, text string) error {
	owner := resolveTransportOwner(ev.Transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallEdit) {
		return fmt.Errorf("transport %q does not support editing", ev.Transport)
	}
	fields := map[string]any{
		"chat_id":    strconv.FormatInt(ev.ChatID, 10),
		"message_id": messageID,
		"text":       text,
		"transport":  ev.Transport,
	}
	_, err := owner.TransportEdit(transportPayload(fields))
	return err
}

// delete message for all
func deleteMessageViaTransport(ev transportEvent, messageID int) error {
	owner := resolveTransportOwner(ev.Transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallDelete) {
		return fmt.Errorf("transport %q does not support deleting", ev.Transport)
	}
	fields := map[string]any{
		"chat_id":    strconv.FormatInt(ev.ChatID, 10),
		"message_id": messageID,
		"transport":  ev.Transport,
	}
	_, err := owner.TransportDelete(transportPayload(fields))
	return err
}

// send photo or gif via transport
func sendPhotoViaTransport(ev transportEvent, path string, caption string, topicID int) error {
	owner := resolveTransportOwner(ev.Transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallMedia) {
		return fmt.Errorf("transport %q does not support sending media", ev.Transport)
	}
	fields := makeTransportPayload(ev, topicID)
	fields["path"] = path
	fields["media_type"] = "photo"
	if caption != "" {
		fields["caption"] = caption
	}
	resp, err := owner.TransportSendMedia(transportPayload(fields))
	if err != nil {
		return err
	}
	var parsed struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal([]byte(resp), &parsed) == nil && parsed.MessageID != "" {
		if id64, convErr := strconv.ParseInt(parsed.MessageID, 10, 64); convErr == nil {
			rememberBotSentMessage(ev.ChatID, int(id64))
		}
	}
	return nil
}

// send voice note via transport. duration is the audio length in seconds as
// reported by the provider; Telegram uses it for the voice note header.
func sendVoiceViaTransport(ev transportEvent, path string, caption string, topicID int, duration float64) error {
	owner := resolveTransportOwner(ev.Transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallMedia) {
		return fmt.Errorf("transport %q does not support sending media", ev.Transport)
	}
	fields := makeTransportPayload(ev, topicID)
	fields["path"] = path
	fields["media_type"] = "voice"
	if duration > 0 {
		fields["duration"] = duration
	}
	if caption != "" {
		fields["caption"] = caption
	}
	resp, err := owner.TransportSendMedia(transportPayload(fields))
	if err != nil {
		return err
	}
	var parsed struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal([]byte(resp), &parsed) == nil && parsed.MessageID != "" {
		if id64, convErr := strconv.ParseInt(parsed.MessageID, 10, 64); convErr == nil {
			rememberBotSentMessage(ev.ChatID, int(id64))
		}
	}
	return nil
}

// send audio file via transport (used when send_as_voice = false)
func sendAudioViaTransport(ev transportEvent, path string, caption string, topicID int, duration float64) error {
	owner := resolveTransportOwner(ev.Transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallMedia) {
		return fmt.Errorf("transport %q does not support sending media", ev.Transport)
	}
	fields := makeTransportPayload(ev, topicID)
	fields["path"] = path
	fields["media_type"] = "audio"
	if duration > 0 {
		fields["duration"] = duration
	}
	if caption != "" {
		fields["caption"] = caption
	}
	resp, err := owner.TransportSendMedia(transportPayload(fields))
	if err != nil {
		return err
	}
	var parsed struct {
		MessageID string `json:"message_id"`
	}
	if json.Unmarshal([]byte(resp), &parsed) == nil && parsed.MessageID != "" {
		if id64, convErr := strconv.ParseInt(parsed.MessageID, 10, 64); convErr == nil {
			rememberBotSentMessage(ev.ChatID, int(id64))
		}
	}
	return nil
}

// mark read via transport
func markReadViaTransport(transport string, chatID int64, messageID int) {
	owner := resolveTransportOwner(transport)
	if owner == nil || !owner.HasNativeTransport() || !owner.TransportSupports(transportCallRead) {
		return
	}
	tName := transport
	if tName == "" {
		tName = owner.TransportName
	}
	_, _ = owner.TransportMarkRead(transportPayload(map[string]any{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"message_id": strconv.Itoa(messageID),
		"transport":  tName,
	}))
}

type transportReadConfig struct {
	GhostMode      bool
	ReadMode       string
	ReadDelayMinMs int
	ReadDelayMaxMs int
}

func getTransportReadConfig(cfg *Config, transport string) transportReadConfig {
	rc := transportReadConfig{
		GhostMode:      false,
		ReadMode:       "smart",
		ReadDelayMinMs: 1200,
		ReadDelayMaxMs: 4000,
	}
	if cfg == nil {
		return rc
	}
	if transport == "" {
		transport = "telegram"
	}
	sec, ok := cfg.PluginSections[transport]
	if !ok {
		return rc
	}
	if gm, ok := sec["ghost_mode"].(bool); ok {
		rc.GhostMode = gm
	}
	if rm, ok := sec["read_mode"].(string); ok && rm != "" {
		rc.ReadMode = strings.ToLower(strings.TrimSpace(rm))
	}
	if minMs, ok := sec["read_delay_min_ms"].(int64); ok && minMs > 0 {
		rc.ReadDelayMinMs = int(minMs)
	}
	if maxMs, ok := sec["read_delay_max_ms"].(int64); ok && maxMs > 0 {
		rc.ReadDelayMaxMs = int(maxMs)
	}
	return rc
}

// mark read with delay and ghost mode
func markReadWithDelay(cfg *Config, transport string, chatID int64, messageID int, onReply bool) {
	rc := getTransportReadConfig(cfg, transport)
	if rc.GhostMode || rc.ReadMode == "never" {
		return
	}
	if !onReply && rc.ReadMode == "on_reply" {
		return
	}
	if rc.ReadMode == "instant" {
		markReadViaTransport(transport, chatID, messageID)
		return
	}
	minMs := rc.ReadDelayMinMs
	maxMs := rc.ReadDelayMaxMs
	if maxMs <= minMs {
		maxMs = minMs + 1000
	}
	delayMs := minMs + rand.Intn(maxMs-minMs+1)
	go func() {
		t := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
		defer t.Stop()
		select {
		case <-t.C:
		case <-shutdownCh:
			return
		}
		markReadViaTransport(transport, chatID, messageID)
	}()
}

// pluginSyscallJSON invokes an addon syscall and parses JSON result
func pluginSyscallJSON(method string, args map[string]any) (map[string]any, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	result, handled := pluginSyscall(method, string(b))
	if handled == 0 {
		return nil, fmt.Errorf("plugin syscall %s not handled", method)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(result), &out); err != nil {
		return nil, fmt.Errorf("plugin syscall %s: invalid response: %w", method, err)
	}
	if status, _ := out["status"].(string); status == "error" {
		if reason, _ := out["reason"].(string); reason != "" {
			return out, fmt.Errorf("%s", reason)
		}
		return out, fmt.Errorf("plugin syscall %s returned error", method)
	}
	return out, nil
}

func telegramSendToUsername(username, text string) error {
	_, err := pluginSyscallJSON("tg.send_to_username", map[string]any{
		"username": username,
		"text":     text,
	})
	return err
}

func telegramResolveUsername(username string) (int64, error) {
	out, err := pluginSyscallJSON("tg.resolve_username", map[string]any{"username": username})
	if err != nil {
		return 0, err
	}
	if id, ok := out["id"].(float64); ok {
		return int64(id), nil
	}
	return 0, fmt.Errorf("telegram: user %q not found", username)
}

func telegramOwnMessageStats() (int, error) {
	out, err := pluginSyscallJSON("tg.stats", map[string]any{})
	if err != nil {
		return 0, err
	}
	count := 0
	if n, ok := out["own_messages"].(float64); ok {
		count = int(n)
	}
	return count, nil
}

func telegramPruneOwnMessages(cutoffUnix int64) int {
	out, err := pluginSyscallJSON("tg.prune_own", map[string]any{"cutoff": cutoffUnix})
	if err != nil {
		return 0
	}
	if n, ok := out["removed"].(float64); ok {
		return int(n)
	}
	return 0
}

// startTransportQRLogin requests QR login from available transport plugins.
// It dispatches generic syscall "transport.start_qr" and falls back to "tg.start_qr".
func startTransportQRLogin() error {
	_, err := pluginSyscallJSON("transport.start_qr", map[string]any{})
	if err != nil {
		_, err = pluginSyscallJSON("tg.start_qr", map[string]any{})
	}
	return err
}

func telegramStartQRLogin() error {
	return startTransportQRLogin()
}

func telegramDownloadCustomEmoji(customEmojiID int64) (string, string, string, error) {
	out, err := pluginSyscallJSON("tg.download_custom_emoji", map[string]any{
		"custom_emoji_id": customEmojiID,
	})
	if err != nil {
		return "", "", "", err
	}
	filePath, _ := out["file"].(string)
	fallback, _ := out["emoji"].(string)
	pack, _ := out["pack"].(string)
	return filePath, fallback, pack, nil
}
