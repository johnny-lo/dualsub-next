package provider

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

var agySample = Request{
	SourceLang: "en",
	TargetLang: "zh-TW",
	Lines: []Line{
		{Index: 1, Text: "Hello"},
		{Index: 2, Text: "World"},
	},
}

func agyErr(t *testing.T, err error) *Error {
	t.Helper()
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	return pe
}

func TestAgyTranslateHappyPath(t *testing.T) {
	p := NewAgy(AgyOptions{Bin: fakeCLI(t, "agy-ok")})
	res, err := p.Translate(context.Background(), agySample)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Lines) != 2 || res.Lines[0].Text != "你好" || res.Lines[1].Text != "世界" {
		t.Errorf("unexpected lines: %+v", res.Lines)
	}
}

func TestAgySuccessIgnoresFailureWordsInTranslation(t *testing.T) {
	res, err := NewAgy(AgyOptions{Bin: fakeCLI(t, "agy-ok-tricky")}).Translate(context.Background(), agySample)
	if err != nil {
		t.Fatalf("a SUCCESS result must not be classified as a failure: %v", err)
	}
	if len(res.Lines) != 2 || res.Lines[0].Text != "速率限制 rate limit 429" {
		t.Errorf("unexpected lines: %+v", res.Lines)
	}
}

func TestAgyTranslateArgs(t *testing.T) {
	bin := fakeCLI(t, "agy-ok")
	launch := recordLaunch(t)
	req := agySample
	req.Model = "gemini-3.8-flash-low"
	if _, err := NewAgy(AgyOptions{Bin: bin}).Translate(context.Background(), req); err != nil {
		t.Fatalf("translate: %v", err)
	}
	l, _ := launch()
	system, user := BuildPrompt(req)
	want := []string{"-p", system + "\n\n" + user, "--output-format", "json", "--print-timeout", "300s", "--model", "gemini-3.8-flash-low"}
	if !reflect.DeepEqual(l.Args, want) {
		t.Errorf("args:\n got %q\nwant %q", l.Args, want)
	}

	if _, err := NewAgy(AgyOptions{Bin: bin}).Translate(context.Background(), agySample); err != nil {
		t.Fatalf("translate: %v", err)
	}
	l, _ = launch()
	for _, a := range l.Args {
		if a == "--model" || a == "--dangerously-skip-permissions" {
			t.Errorf("unexpected %s in %q", a, l.Args)
		}
	}
}

func TestAgyRunsInFreshEmptyDirRemovedAfter(t *testing.T) {
	bin := fakeCLI(t, "agy-ok")
	launch := recordLaunch(t)
	if _, err := NewAgy(AgyOptions{Bin: bin}).Translate(context.Background(), agySample); err != nil {
		t.Fatalf("translate: %v", err)
	}
	l, _ := launch()
	wd, _ := os.Getwd()
	if l.Cwd == wd {
		t.Errorf("agy ran in the daemon's own directory %q", wd)
	}
	if l.CwdEntries != 0 {
		t.Errorf("work dir had %d entries at launch, want 0", l.CwdEntries)
	}
	if _, err := os.Stat(l.Cwd); !os.IsNotExist(err) {
		t.Errorf("work dir %q still exists after Translate (stat err %v)", l.Cwd, err)
	}
}

func TestAgyPromptPassedVerbatim(t *testing.T) {
	bin := fakeCLI(t, "agy-ok")
	launch := recordLaunch(t)
	req := agySample
	req.Lines = []Line{{Index: 1, Text: "Tom & \"Jerry\" | 100% 'ok'\nnext"}, {Index: 2, Text: "$HOME ^ < >"}}
	_, _ = NewAgy(AgyOptions{Bin: bin}).Translate(context.Background(), req)
	l, ok := launch()
	if !ok {
		t.Fatal("agy was not launched")
	}
	system, user := BuildPrompt(req)
	if len(l.Args) < 2 || l.Args[1] != system+"\n\n"+user {
		t.Errorf("prompt was altered on its way to agy:\n got %q", l.Args)
	}
}

func TestAgyPromptTooLong(t *testing.T) {
	bin := fakeCLI(t, "agy-ok")
	launch := recordLaunch(t)
	req := agySample
	req.Lines = []Line{{Index: 1, Text: strings.Repeat("x", agyMaxPromptChars+1)}}
	_, err := NewAgy(AgyOptions{Bin: bin}).Translate(context.Background(), req)
	pe := agyErr(t, err)
	if pe.Code != CodeContextTooLong || pe.Retryable {
		t.Errorf("got %s retryable=%v, want non-retryable CONTEXT_TOO_LONG", pe.Code, pe.Retryable)
	}
	if _, launched := launch(); launched {
		t.Error("agy was launched for an oversized prompt")
	}
}

func TestAgyTranslateErrors(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		code      string
		retryable bool
	}{
		{"agy-signin", CodeMissingConfig, false},
		{"agy-badmodel", CodeBadRequest, false},
		{"agy-ratelimit", CodeRateLimit, true},
		{"agy-status-error", CodeServerError, true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			_, err := NewAgy(AgyOptions{Bin: fakeCLI(t, tc.mode)}).Translate(context.Background(), agySample)
			pe := agyErr(t, err)
			if pe.Code != tc.code || pe.Retryable != tc.retryable {
				t.Errorf("got %s retryable=%v, want %s retryable=%v (%s)", pe.Code, pe.Retryable, tc.code, tc.retryable, pe.Message)
			}
		})
	}
}

func TestAgyRejectsUnsafeModel(t *testing.T) {
	bin := fakeCLI(t, "agy-ok")
	launch := recordLaunch(t)
	req := agySample
	req.Model = "x; rm"
	_, err := NewAgy(AgyOptions{Bin: bin}).Translate(context.Background(), req)
	if pe := agyErr(t, err); pe.Code != CodeBadRequest || pe.Retryable {
		t.Errorf("got %s retryable=%v, want non-retryable BAD_REQUEST", pe.Code, pe.Retryable)
	}
	if _, launched := launch(); launched {
		t.Error("agy was launched with an unsafe model")
	}
}

func TestParseAgyModels(t *testing.T) {
	got := parseAgyModels("gemini-3.8-flash-low\tGemini 3.8 Flash (Low)\n\nno tab here\nclaude-sonnet-4-6\tClaude Sonnet 4.6 (Thinking)\r\n")
	want := []ModelInfo{
		{ID: "gemini-3.8-flash-low", Label: "Gemini 3.8 Flash (Low)"},
		{ID: "claude-sonnet-4-6", Label: "Claude Sonnet 4.6 (Thinking)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestAgyListModels(t *testing.T) {
	got, err := NewAgy(AgyOptions{Bin: fakeCLI(t, "agy-models")}).(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != "gemini-3.8-flash-low" {
		t.Errorf("unexpected models: %+v", got)
	}
}

func TestAgyListModelsSignedOut(t *testing.T) {
	_, err := NewAgy(AgyOptions{Bin: fakeCLI(t, "agy-models-signin")}).(ModelLister).ListModels(context.Background())
	pe := agyErr(t, err)
	if pe.Code != CodeMissingConfig || pe.Message != "agy is not signed in" {
		t.Errorf("got %s %q", pe.Code, pe.Message)
	}
}

func TestAgyMissingBin(t *testing.T) {
	_, err := NewAgy(AgyOptions{Bin: "/nonexistent/agy-xyz-does-not-exist"}).Translate(context.Background(), agySample)
	if pe := agyErr(t, err); pe.Code != CodeMissingConfig {
		t.Errorf("got %s, want MISSING_CONFIG", pe.Code)
	}
}

func TestAgyNameAndModel(t *testing.T) {
	p := NewAgy(AgyOptions{Model: "gemini-3.8-flash-low"})
	if p.Name() != "agy" || p.DefaultModel() != "gemini-3.8-flash-low" {
		t.Errorf("got %q / %q", p.Name(), p.DefaultModel())
	}
}
