package logger

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/chrischapin/discord-cli/internal/consts"
)

const fileName = "logs.txt"

func DefaultPath() string {
	return filepath.Join(consts.CacheDir(), fileName)
}

// Load opens the log file and configures default logger.
func Load(path string, level slog.Level) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	// Append rather than truncate so logs survive across runs, and use 0o644 so
	// the log isn't world-writable.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	opts := &slog.HandlerOptions{Level: level}
	handler := slog.NewTextHandler(file, opts)
	slog.SetDefault(slog.New(handler))
	return nil
}
