package humanify

import "testing"

func TestLoadTypoChancePercent(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]any
		want float64
	}{
		{"percent 35", map[string]any{"typo_chance_percent": float64(35)}, 0.35},
		{"percent 50", map[string]any{"typo_chance_percent": int64(50)}, 0.50},
		{"percent 75", map[string]any{"typo_chance_percent": int(75)}, 0.75},
		{"fraction", map[string]any{"typo_chance_percent": 0.35}, 0.35},
		{"legacy fraction", map[string]any{"typo_chance": 0.35}, 0.35},
		{"legacy percent", map[string]any{"typo_chance": float64(35)}, 0.35},
		{"string percent", map[string]any{"typo_chance_percent": "35%"}, 0.35},
		{"clamped high", map[string]any{"typo_chance_percent": float64(300)}, 1},
		{"clamped low", map[string]any{"typo_chance_percent": float64(-10)}, 0},
		{"percent wins", map[string]any{
			"typo_chance_percent": float64(35),
			"typo_chance":         float64(0.05),
		}, 0.35},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Load(tc.in)
			if cfg.TypoChance != tc.want {
				t.Fatalf("TypoChance = %v, want %v", cfg.TypoChance, tc.want)
			}
		})
	}
}

func TestLoadTypoDefault(t *testing.T) {
	if got := Load(nil).TypoChance; got != 0.35 {
		t.Fatalf("default TypoChance = %v, want 0.35", got)
	}
}
