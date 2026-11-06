package main

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
)

func describeSticker(ctx context.Context, path string, provider LLMProvider, cfg *Config, log *Logger) string {
	b, err := os.ReadFile(path)
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
