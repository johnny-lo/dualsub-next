package provider

import (
	"context"
	"os"
	"reflect"
	"testing"
)

var codexSample = Request{
	SourceLang: "en",
	TargetLang: "zh-TW",
	Lines: []Line{
		{Index: 1, Text: "Hello"},
		{Index: 2, Text: "World"},
	},
}

func TestCodexTranslateHappyPath(t *testing.T) {
	bin := fakeCodex(t, "ok")
	p := NewCodex(CodexOptions{Bin: bin})
	res, err := p.Translate(context.Background(), codexSample)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Lines) != 2 || res.Lines[0].Text != "你好" || res.Lines[1].Text != "世界" {
		t.Errorf("unexpected lines: %+v", res.Lines)
	}
}

func TestCodexTranslateRateLimit(t *testing.T) {
	bin := fakeCodex(t, "ratelimit")
	p := NewCodex(CodexOptions{Bin: bin})
	_, err := p.Translate(context.Background(), codexSample)
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if pe.Code != CodeRateLimit {
		t.Errorf("got %s, want PROVIDER_RATE_LIMIT", pe.Code)
	}
	if !pe.Retryable {
		t.Error("rate limit should be retryable")
	}
}

func TestCodexTranslateMissingBin(t *testing.T) {
	p := NewCodex(CodexOptions{Bin: "/nonexistent/codex-xyz-does-not-exist"})
	_, err := p.Translate(context.Background(), codexSample)
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T: %v", err, err)
	}
	if pe.Code != CodeMissingConfig {
		t.Errorf("got %s, want MISSING_CONFIG", pe.Code)
	}
}

func TestCodexNameAndModel(t *testing.T) {
	p := NewCodex(CodexOptions{Model: "gpt-5"})
	if p.Name() != "codex" {
		t.Errorf("name: got %q", p.Name())
	}
	if p.DefaultModel() != "gpt-5" {
		t.Errorf("default model: got %q", p.DefaultModel())
	}
}

var allEfforts = []string{"low", "medium", "high", "xhigh", "max", "ultra"}

func TestParseCodexModelsFiltersHiddenAndSortsByPriority(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_models.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseCodexModels(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []ModelInfo{
		{ID: "gpt-6-astra", Label: "GPT-6-Astra", Efforts: allEfforts, DefaultEffort: "low"},
		{ID: "gpt-5.6-sol", Label: "GPT-5.6-Sol", Efforts: allEfforts, DefaultEffort: "low"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseCodexModelsRejectsGarbage(t *testing.T) {
	_, err := parseCodexModels([]byte("not json"))
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodeParseFailed {
		t.Fatalf("want PARSE_FAILED *Error, got %T: %v", err, err)
	}
}

func TestCodexListModelsRunsDebugModels(t *testing.T) {
	bin := fakeCLI(t, "codex-models")
	useFixture(t, "codex_models.json")
	got, err := NewCodex(CodexOptions{Bin: bin}).(ModelLister).ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != "gpt-6-astra" || got[1].ID != "gpt-5.6-sol" {
		t.Errorf("unexpected models: %+v", got)
	}
}

func TestCodexTranslatePassesEffort(t *testing.T) {
	bin := fakeCLI(t, "args")
	launch := recordLaunch(t)
	req := codexSample
	req.Model, req.Effort = "gpt-5.6-sol", "low"
	if _, err := NewCodex(CodexOptions{Bin: bin}).Translate(context.Background(), req); err != nil {
		t.Fatalf("translate: %v", err)
	}
	l, ok := launch()
	if !ok {
		t.Fatal("fake codex was not launched")
	}
	if !hasArgPair(l.Args, "-m", "gpt-5.6-sol") {
		t.Errorf("missing -m gpt-5.6-sol in %q", l.Args)
	}
	if !hasArgPair(l.Args, "-c", "model_reasoning_effort=low") {
		t.Errorf("missing -c model_reasoning_effort=low in %q", l.Args)
	}
}

func TestCodexTranslateOmitsEffortWhenEmpty(t *testing.T) {
	bin := fakeCLI(t, "args")
	launch := recordLaunch(t)
	if _, err := NewCodex(CodexOptions{Bin: bin}).Translate(context.Background(), codexSample); err != nil {
		t.Fatalf("translate: %v", err)
	}
	l, _ := launch()
	for _, a := range l.Args {
		if a == "-c" {
			t.Fatalf("unexpected -c in %q", l.Args)
		}
	}
}

func TestCodexRejectsUnsafeEffortAndModel(t *testing.T) {
	for _, tc := range []struct{ model, effort string }{
		{"gpt-5.6-sol", "low & calc"},
		{"a b", ""},
	} {
		bin := fakeCLI(t, "args")
		launch := recordLaunch(t)
		req := codexSample
		req.Model, req.Effort = tc.model, tc.effort
		_, err := NewCodex(CodexOptions{Bin: bin}).Translate(context.Background(), req)
		pe, ok := err.(*Error)
		if !ok || pe.Code != CodeBadRequest || pe.Retryable {
			t.Errorf("%+v: want non-retryable BAD_REQUEST, got %T: %v", tc, err, err)
		}
		if _, launched := launch(); launched {
			t.Errorf("%+v: codex was launched", tc)
		}
	}
}

func TestCodexListModelsEmptyIsAnError(t *testing.T) {
	// Every model hidden: an empty picker must not count as a successful list.
	bin := fakeCLI(t, "codex-models")
	t.Setenv(fakeFixtureEnv, "")
	_, err := NewCodex(CodexOptions{Bin: bin}).(ModelLister).ListModels(context.Background())
	pe, ok := err.(*Error)
	if !ok || pe.Code != CodeServerError {
		t.Fatalf("want SERVER_ERROR *Error, got %T: %v", err, err)
	}
}
