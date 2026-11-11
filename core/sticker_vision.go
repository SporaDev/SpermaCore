package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"

	"spermacore/core/media"
)

func describeSticker(ctx context.Context, path string, provider LLMProvider, cfg *Config, log *Logger) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	ext := strings.ToLower(filepath.Ext(path))
	// Telegram Lottie stickers cannot be decoded without a dedicated renderer.
	if ext == ".tgs" {
		log.Debug("sticker vision: skipping lottie sticker %s", path)
		return ""
	}
	imagePath := path
	// Animated stickers (webm) and stray videos have no direct image stream;
	// describe their first frame instead.
	switch ext {
	case ".webm", ".mp4", ".mov", ".mkv", ".gif":
		frames, err := media.AnalyzeVideoFrames(ctx, path)
		if err != nil || len(frames) == 0 {
			if err != nil {
				log.Warn("sticker vision: frame extraction for %s: %v", path, err)
			}
			return ""
		}
		defer os.RemoveAll(filepath.Dir(frames[0]))
		imagePath = frames[0]
	}

	b, err := os.ReadFile(imagePath)
	if err != nil {
		return ""
	}
	model := cfg.ModelFor("chat")
	if cfg.HasRoute("vision") {
		model = cfg.ModelFor("vision")
	}
	defaultStickerPrompt := "Describe this sticker briefly for a local sticker catalog. Include emotion, action, character/object, and situations where it fits. Return only the description."
	systemPrompt := loadPromptFile(cfg.Core.DataDir, "vision_sticker.txt", defaultStickerPrompt)
	msg := []ChatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: "Sticker image:", Images: []string{base64.StdEncoding.EncodeToString(b)}}}
	out, err := provider.Chat(ctx, model, msg, nil)
	if err != nil {
		log.Warn("sticker vision: %v", err)
		return ""
	}
	return strings.TrimSpace(out.Content)
}
