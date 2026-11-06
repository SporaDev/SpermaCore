package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"spermacore/core/media"
)

func describeEmoji(ctx context.Context, path string, fallbackEmoji string, pack string, provider LLMProvider, cfg *Config, log *Logger) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	model := cfg.ModelFor("chat")
	if cfg.HasRoute("vision") {
		model = cfg.ModelFor("vision")
	}

	userPrompt := "Custom emoji image:"
	if fallbackEmoji != "" {
		userPrompt += fmt.Sprintf(" (standard fallback: %s)", fallbackEmoji)
	}
	if pack != "" {
		userPrompt += fmt.Sprintf(" (pack: %s)", pack)
	}

	defaultEmojiPrompt := "Describe this custom Telegram emoji briefly for a local emoji catalog (emoji.json). " +
		"Include what character or object is shown, emotion or visual vibe, and conversational context or situations " +
		"where this emoji fits well as an emotional reaction or inline expression. Return only the concise description (1-2 sentences in Russian or English)."
	systemPrompt := loadPromptFile(cfg.Core.DataDir, "vision_emoji.txt", defaultEmojiPrompt)

	msg := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt, Images: []string{base64.StdEncoding.EncodeToString(b)}},
	}
	out, err := provider.Chat(ctx, model, msg, nil)
	if err != nil {
		log.Warn("emoji vision: %v", err)
		return ""
	}
	return strings.TrimSpace(out.Content)
}

func catalogCustomEmoji(ctx context.Context, customEmojiID int64, fallbackEmoji string, pack string, provider LLMProvider, cfg *Config, log *Logger) {
	if customEmojiID <= 0 {
		return
	}
	idStr := strconv.FormatInt(customEmojiID, 10)
	existing := media.FindEmoji(cfg.Core.DataDir, idStr)
	if existing != nil && strings.TrimSpace(existing.Description) != "" {
		return
	}

	filePath, fb, pk, err := telegramDownloadCustomEmoji(customEmojiID)
	if err != nil || filePath == "" {
		log.Debug("catalogCustomEmoji %d: download failed: %v", customEmojiID, err)
		return
	}
	if fb != "" && fallbackEmoji == "" {
		fallbackEmoji = fb
	}
	if pk != "" && pack == "" {
		pack = pk
	}

	desc := describeEmoji(ctx, filePath, fallbackEmoji, pack, provider, cfg, log)
	if desc == "" {
		log.Warn("catalogCustomEmoji %d: vision description empty", customEmojiID)
		return
	}

	rec := media.EmojiRecord{
		ID:            idStr,
		CustomEmojiID: customEmojiID,
		Emoji:         fallbackEmoji,
		Pack:          pack,
		Description:   desc,
		File:          filePath,
		UpdatedAt:     time.Now(),
	}
	media.RememberEmoji(cfg.Core.DataDir, rec)
	log.Info("custom emoji catalogued id=%d emoji=%s desc=%q", customEmojiID, fallbackEmoji, desc)
}
