// Package logging sets up structured (JSON) logs with size-based rotation
// in the user's log directory.
//
// Secrets policy: subscription URLs (secret panel prefix + token) are never
// logged in full — callers log subscription.Redact(url).
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// FileName is the active log file in the log dir.
const FileName = "akari-client.log"

// Options configure the logger.
type Options struct {
	Dir   string
	Level slog.Level
	// Stderr additionally writes human-readable logs to stderr (dev runs).
	Stderr bool
	// MaxSizeMB / MaxBackups / MaxAgeDays bound disk use (defaults 10/5/14).
	MaxSizeMB, MaxBackups, MaxAgeDays int
}

// ParseLevel maps "debug", "info", "warn", "error" (default info).
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New returns the logger and a closer for the rotating file.
func New(o Options) (*slog.Logger, io.Closer) {
	if o.MaxSizeMB <= 0 {
		o.MaxSizeMB = 10
	}
	if o.MaxBackups <= 0 {
		o.MaxBackups = 5
	}
	if o.MaxAgeDays <= 0 {
		o.MaxAgeDays = 14
	}
	rot := &lumberjack.Logger{
		Filename:   filepath.Join(o.Dir, FileName),
		MaxSize:    o.MaxSizeMB,
		MaxBackups: o.MaxBackups,
		MaxAge:     o.MaxAgeDays,
		LocalTime:  false,
		Compress:   true,
	}
	opts := &slog.HandlerOptions{Level: o.Level}
	var h slog.Handler = slog.NewJSONHandler(rot, opts)
	if o.Stderr {
		h = fanout{h, slog.NewTextHandler(os.Stderr, opts)}
	}
	return slog.New(h), rot
}

type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (f fanout) WithAttrs(a []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(a)
	}
	return out
}

func (f fanout) WithGroup(n string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(n)
	}
	return out
}
