package provider

import (
	"strings"
	"testing"
)

func TestBuildPrompt(t *testing.T) {
	in := Request{
		SourceLang: "en",
		TargetLang: "zh-TW",
		Lines: []Line{
			{Index: 1, Text: "Hello"},
			{Index: 2, Text: "World"},
		},
	}
	system, user := BuildPrompt(in)
	if !strings.Contains(system, "en") || !strings.Contains(system, "zh-TW") {
		t.Errorf("system missing langs: %s", system)
	}
	if user != "[1] Hello\n[2] World" {
		t.Errorf("user prompt unexpected: %q", user)
	}
}

func TestParseResponse_Complete(t *testing.T) {
	expected := []Line{
		{Index: 1, Text: "Hello"},
		{Index: 2, Text: "World"},
	}
	out, err := ParseResponse("[1] 你好\n[2] 世界", expected, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 || out[0].Text != "你好" || out[1].Text != "世界" {
		t.Errorf("unexpected: %+v", out)
	}
}

func TestParseResponse_NoisyOutput(t *testing.T) {
	// LLMs sometimes wrap output in commentary; we should still extract.
	expected := []Line{{Index: 1, Text: "Hello"}, {Index: 2, Text: "World"}}
	raw := "Sure, here you go:\n\n[1] 你好\n[2] 世界\n\nLet me know if you need more."
	out, err := ParseResponse(raw, expected, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d, want 2", len(out))
	}
}

func TestParseResponse_Missing(t *testing.T) {
	expected := []Line{
		{Index: 1, Text: "Hello"},
		{Index: 2, Text: "World"},
		{Index: 3, Text: "!"},
	}
	_, err := ParseResponse("[1] 你好\n[3] ！", expected, "test")
	if err == nil {
		t.Fatal("expected error for missing index")
	}
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if pe.Code != CodeParseFailed {
		t.Errorf("got %s, want PARSE_FAILED", pe.Code)
	}
	if !pe.Retryable {
		t.Error("PARSE_FAILED should be retryable")
	}
}

func TestParseResponse_ExtraIgnored(t *testing.T) {
	expected := []Line{{Index: 1, Text: "Hello"}}
	out, err := ParseResponse("[1] 你好\n[2] 多餘的", expected, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d, want 1", len(out))
	}
}

func TestMapStatus(t *testing.T) {
	cases := []struct {
		status int
		body   string
		code   string
		retry  bool
	}{
		{401, "unauthorized", CodeInvalidKey, false},
		{403, "forbidden", CodeInvalidKey, false},
		{429, "too many requests", CodeRateLimit, true},
		{500, "internal error", CodeServerError, true},
		{502, "bad gateway", CodeServerError, true},
		{504, "gateway timeout", CodeTimeout, true},
		{400, "context length exceeded", CodeContextTooLong, false},
		{400, "bad json", CodeBadRequest, false},
	}
	for _, c := range cases {
		err := mapStatus("test", c.status, []byte(c.body))
		pe, ok := err.(*Error)
		if !ok {
			t.Fatalf("status %d: not *Error: %T", c.status, err)
		}
		if pe.Code != c.code {
			t.Errorf("status %d body %q: code %s, want %s", c.status, c.body, pe.Code, c.code)
		}
		if pe.Retryable != c.retry {
			t.Errorf("status %d: retryable %v, want %v", c.status, pe.Retryable, c.retry)
		}
	}
}

func TestClaudeStubReturnsNotImplemented(t *testing.T) {
	p := NewClaude(ClaudeOptions{})
	_, err := p.Translate(nil, Request{})
	if err == nil {
		t.Fatal("expected error from claude stub")
	}
	pe := err.(*Error)
	if pe.Code != CodeNotImplemented {
		t.Errorf("got %s, want NOT_IMPLEMENTED", pe.Code)
	}
}
