package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AnalyzeVideoFrames extracts up to 12 frames from video using ffmpeg.
func AnalyzeVideoFrames(ctx context.Context, videoPath string) ([]string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, fmt.Errorf("ffmpeg not found: %w", err)
	}
	dir := filepath.Join(os.TempDir(), "spermacore_vision",
		fmt.Sprintf("frames_%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	pattern := filepath.Join(dir, "frame_%03d.jpg")
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", videoPath,
		"-vf", "fps=1", "-frames:v", "12", pattern)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ffmpeg frames: %w: %s", err, strings.TrimSpace(string(output)))
	}
	matches, err := filepath.Glob(filepath.Join(dir, "frame_*.jpg"))
	if err != nil || len(matches) == 0 {
		return nil, fmt.Errorf("no video frames extracted")
	}
	return matches, nil
}
