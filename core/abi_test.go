package main

import (
	"strings"
	"testing"
)

func TestCheckPluginABI(t *testing.T) {
	tests := []struct {
		name       string
		major      uint32
		minor      uint32
		structSize uint32
		wantErr    string
		wantWarn   string
	}{
		{
			name:       "same version",
			major:      1,
			minor:      7,
			structSize: 144,
		},
		{
			name:       "major mismatch",
			major:      2,
			minor:      0,
			structSize: 144,
			wantErr:    "major mismatch",
		},
		{
			name:       "struct too small",
			major:      1,
			minor:      0,
			structSize: 8,
			wantErr:    "too small",
		},
		{
			name:       "plugin newer minor",
			major:      1,
			minor:      99,
			structSize: 160,
			wantWarn:   "newer than core",
		},
		{
			name:       "plugin older minor",
			major:      1,
			minor:      0,
			structSize: 144,
			wantWarn:   "older than core",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warn, err := checkPluginABI(tt.major, tt.minor, tt.structSize)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if tt.wantWarn != "" && !strings.Contains(warn, tt.wantWarn) {
				t.Fatalf("warn = %q, want containing %q", warn, tt.wantWarn)
			}
			if tt.wantWarn == "" && warn != "" {
				t.Fatalf("unexpected warn: %q", warn)
			}
		})
	}
}
