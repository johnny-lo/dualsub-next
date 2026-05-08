// Package logger writes line-delimited JSON events to a file. It is intentionally
// minimal — no rotation, no levels — since the daemon is single-user and the
// user is expected to wipe the log when it grows.
package logger

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// New opens (or creates) the log file in append mode. If path is empty, the
// returned Logger writes only to stderr.
func New(path string) (*Logger, error) {
	if path == "" {
		return &Logger{w: os.Stderr}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Logger{w: io.MultiWriter(os.Stderr, f)}, nil
}

// Event writes one JSON-encoded record. fields is merged with default keys.
func (l *Logger) Event(kind string, fields map[string]any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := map[string]any{
		"ts":   time.Now().UTC().Format(time.RFC3339Nano),
		"kind": kind,
	}
	for k, v := range fields {
		rec[k] = v
	}
	enc := json.NewEncoder(l.w)
	if err := enc.Encode(rec); err != nil {
		log.Printf("logger encode: %v", err)
	}
}
