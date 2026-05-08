package server

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// writeSSE writes a single Server-Sent Event in `event:`/`data:` form.
// Multi-line JSON is split onto multiple `data:` lines per the SSE spec.
func writeSSE(w io.Writer, event string, payload any) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	for _, line := range strings.Split(string(buf), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = fmt.Fprint(w, "\n")
	return err
}
