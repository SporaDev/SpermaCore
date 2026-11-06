package main

import (
	"path/filepath"
	"reflect"

	"spermacore/core/humanify"
	"spermacore/core/webui"
)

// applyRuntimeConfig applies dynamically reloadable settings and reports fields requiring restart
func applyRuntimeConfig(old, cur *Config, log *Logger) []string {
	if cur == nil {
		return nil
	}
	if log == nil {
		log = GetLogger("core.config")
	}

	// reload humanify
	hcfg := humanify.Load(cur.RootSections["humanify"])
	humanify.Init(hcfg)
	log.Info("hot-reload: humanify enabled=%v delay=%v..%v split=%v typo=%.0f%%",
		hcfg.Enabled, hcfg.DelayMin, hcfg.DelayMax, hcfg.SplitLong, hcfg.TypoChance*100)

	// reload grouper
	gcfg := LoadGrouperConfig(cur.RootSections["grouper"])
	UpdateGrouperConfig(gcfg)
	log.Info("hot-reload: grouper enabled=%v window=%v max_wait=%v",
		gcfg.Enabled, gcfg.Window, gcfg.MaxWait)

	// reload group policy
	loadGroupPolicy(filepath.Dir(cur.SourcePath), cur.RootSections["groups"], log)

	// reload chat context config
	loadChatContextConfig(cur.RootSections["context"])
	log.Info("hot-reload: context and group policy reloaded")

	return restartRequired(old, cur)
}

// restartRequired checks for changes in fields requiring restart
func restartRequired(old, cur *Config) []string {
	if old == nil || cur == nil {
		return nil
	}
	var out []string
	add := func(field string, a, b any) {
		if !reflect.DeepEqual(a, b) {
			out = append(out, field)
		}
	}

	add("core.workers", old.Core.Workers, cur.Core.Workers)
	add("core.data_dir", old.Core.DataDir, cur.Core.DataDir)
	add("llm.provider", old.LLM.Provider, cur.LLM.Provider)
	add("llm.model", old.LLM.Model, cur.LLM.Model)
	add("memory.embedding_model", old.Memory.EmbeddingModel, cur.Memory.EmbeddingModel)
	add("plugins.load", old.Plugins.Load, cur.Plugins.Load)

	ow, cw := webui.Load(old.RootSections["webui"]), webui.Load(cur.RootSections["webui"])
	add("webui.enabled", ow.Enabled, cw.Enabled)
	add("webui.bind", ow.Bind, cw.Bind)
	add("webui.port", ow.Port, cw.Port)

	return out
}
