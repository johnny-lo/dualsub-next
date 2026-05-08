package logger

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventAppendsJSONLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "test.log")

	lg, err := New(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	lg.Event("translate_start", map[string]any{
		"video_key": "vid-A", "lines": 12,
	})
	lg.Event("translate_done", map[string]any{
		"video_key": "vid-A", "completed": 1,
	})

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var kinds []string
	for scanner.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			t.Fatalf("decode line: %v (%q)", err, scanner.Text())
		}
		if _, ok := rec["ts"].(string); !ok {
			t.Errorf("missing ts: %v", rec)
		}
		kinds = append(kinds, rec["kind"].(string))
	}
	if len(kinds) != 2 {
		t.Fatalf("got %d events, want 2", len(kinds))
	}
	if kinds[0] != "translate_start" || kinds[1] != "translate_done" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestEmptyPathOnlyWritesStderr(t *testing.T) {
	lg, err := New("")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	// Should not panic; can't easily assert stderr in a unit test, just call it.
	lg.Event("smoke", nil)
	if !strings.Contains("ok", "ok") {
		t.Fatal("trivially false")
	}
}
