package provider

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// The Gemini API key travels in the URL query, and net/http embeds the URL in
// transport errors. sanitizeMessage scrubs it from Error.Message, but Error()
// also prints the Cause, which must not leak the key either: the shared-cache
// server returns err.Error() to clients and writes it to its log.
func TestErrorStringRedactsAPIKeyFromCause(t *testing.T) {
	const secret = "AIzaSECRET123"
	raw := &url.Error{
		Op:  "Post",
		URL: "https://generativelanguage.googleapis.com/v1beta/models/m:generateContent?key=" + secret,
		Err: errors.New("dial tcp: connection refused"),
	}
	err := mapNetErr("gemini", raw)
	if got := err.Error(); strings.Contains(got, secret) {
		t.Fatalf("Error() leaks API key: %s", got)
	}
}
