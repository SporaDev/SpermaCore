// package humanify adds human-like delays, typing jitter, typos, and message splitting.
package humanify

import (
	"strconv"
	"strings"
	"time"
)

// defaults for config.toml
var Defaults = map[string]any{
	"enabled": true,

	"delay_min_ms":       1200,
	"delay_max_ms":       3500,
	"distraction_chance": 0.08,
	"distraction_max_ms": 6000,
	// percent chance of typo (0-100)
	"typo_chance_percent": 35,
	"typo_max":            1,
	"typing_jitter":       0.3,

	"split_long":        true,
	"split_threshold":   200,
	"split_parts":       3,
	"split_pause_ms":    2200,
	"reply_probability": 0.35,
}

// humanify config
type Config struct {
	Enabled bool

	DelayMin          time.Duration
	DelayMax          time.Duration
	DistractionChance float64
	DistractionMax    time.Duration
	TypoChance        float64
	TypoMax           int
	AutoFixTypo       bool
	TypingJitter      float64

	SplitLong        bool
	SplitThreshold   int
	SplitParts       int
	SplitPause       time.Duration
	ReplyProbability float64
}

// load config from map or defaults
func Load(section map[string]any) *Config {
	c := &Config{
		Enabled:           true,
		DelayMin:          1200 * time.Millisecond,
		DelayMax:          3500 * time.Millisecond,
		DistractionChance: 0.08,
		DistractionMax:    6 * time.Second,
		TypoChance:        0.35,
		TypoMax:           1,
		AutoFixTypo:       true,
		TypingJitter:      0.3,
		SplitLong:         true,
		SplitThreshold:    200,
		SplitParts:        3,
		SplitPause:        2200 * time.Millisecond,
		ReplyProbability:  0.35,
	}
	if section == nil {
		return c
	}

	if v, ok := getBool(section, "enabled"); ok {
		c.Enabled = v
	}
	if v, ok := getInt(section, "delay_min_ms"); ok && v >= 0 {
		c.DelayMin = time.Duration(v) * time.Millisecond
	}
	if v, ok := getInt(section, "delay_max_ms"); ok && v >= 0 {
		c.DelayMax = time.Duration(v) * time.Millisecond
	}
	if v, ok := getFloat(section, "typing_jitter"); ok && v >= 0 {
		c.TypingJitter = v
	}
	if v, ok := getFloat(section, "distraction_chance"); ok && v >= 0 && v <= 1 {
		c.DistractionChance = v
	}
	if v, ok := getInt(section, "distraction_max_ms"); ok && v >= 0 {
		c.DistractionMax = time.Duration(v) * time.Millisecond
	}
	if v, ok := getChance(section, "typo_chance_percent"); ok {
		c.TypoChance = normalizeChance(v)
	} else if v, ok := getChance(section, "typo_chance"); ok {
		// legacy key compatibility
		c.TypoChance = normalizeChance(v)
	}
	if v, ok := getInt(section, "typo_max"); ok && v >= 0 {
		c.TypoMax = int(v)
	}
	if v, ok := getBool(section, "auto_fix_typo"); ok {
		c.AutoFixTypo = v
	}
	if v, ok := getBool(section, "split_long"); ok {
		c.SplitLong = v
	}
	if v, ok := getInt(section, "split_threshold"); ok && v > 0 {
		c.SplitThreshold = int(v)
	}
	if v, ok := getInt(section, "split_parts"); ok && v > 0 {
		c.SplitParts = int(v)
	}
	if v, ok := getInt(section, "split_pause_ms"); ok && v >= 0 {
		c.SplitPause = time.Duration(v) * time.Millisecond
	}
	if v, ok := getFloat(section, "reply_probability"); ok && v >= 0 && v <= 1 {
		c.ReplyProbability = v
	}

	if c.DelayMax < c.DelayMin {
		c.DelayMax = c.DelayMin
	}
	return c
}

// normalize value to 0..1 range
func normalizeChance(v float64) float64 {
	if v > 1 {
		v /= 100
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func getBool(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func getInt(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		return int64(x), true
	}
	return 0, false
}

// get chance parses fractional or percentage value
func getChance(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case string:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(x), "%"))
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func getFloat(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}
