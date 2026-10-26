package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

// CodexOptions configures the codex CLI provider. No API key is required —
// auth is handled by the user's existing `codex login`.
type CodexOptions struct {
	Bin     string        // default "codex"
	Profile string        // optional → codex exec -p
	Model   string        // optional → codex exec -m
	Sandbox string        // default "read-only" → codex exec -s
	Timeout time.Duration // default 4m (agents are slower than HTTP APIs)
}

type codexProvider struct {
	opts CodexOptions
}

func NewCodex(opts CodexOptions) Provider {
	if opts.Bin == "" {
		opts.Bin = "codex"
	}
	if opts.Sandbox == "" {
		opts.Sandbox = "read-only"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 4 * time.Minute
	}
	return &codexProvider{opts: opts}
}

func (p *codexProvider) Name() string { return "codex" }

func (p *codexProvider) DefaultModel() string { return p.opts.Model }

// cliRateLimitRE detects subscription rate-limit / quota messages from a CLI.
var cliRateLimitRE = regexp.MustCompile(`(?i)rate.?limit|quota|usage limit|too many requests|\b429\b`)

func (p *codexProvider) Translate(ctx context.Context, in Request) (Response, error) {
	model := in.Model
	if model == "" {
		model = p.opts.Model
	}
	if model != "" && !validCLIValue(model) {
		return Response{}, &Error{Code: CodeBadRequest, Provider: "codex", Message: fmt.Sprintf("invalid model %q", model)}
	}
	if in.Effort != "" && !validCLIValue(in.Effort) {
		return Response{}, &Error{Code: CodeBadRequest, Provider: "codex", Message: fmt.Sprintf("invalid effort %q", in.Effort)}
	}

	bin, err := exec.LookPath(p.opts.Bin)
	if err != nil {
		return Response{}, &Error{
			Code: CodeMissingConfig, Provider: "codex",
			Message: "codex CLI not found on PATH: " + p.opts.Bin, Cause: err,
		}
	}

	system, user := BuildPrompt(in)
	prompt := system + "\n\n" + user

	// codex exec -o writes ONLY the agent's final message here, so we never
	// have to strip reasoning / tool logs from stdout.
	outFile, err := os.CreateTemp("", "dualsub-codex-*.txt")
	if err != nil {
		return Response{}, &Error{Code: CodeServerError, Provider: "codex", Message: "create temp file: " + err.Error(), Cause: err}
	}
	outPath := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outPath)

	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()

	args := []string{"exec", "--skip-git-repo-check", "-s", p.opts.Sandbox, "--color", "never", "-o", outPath}
	if p.opts.Profile != "" {
		args = append(args, "-p", p.opts.Profile)
	}
	if model != "" {
		args = append(args, "-m", model)
	}
	if in.Effort != "" {
		// Unquoted on purpose: codex reads a value that is not valid TOML as a
		// literal string, and this avoids quoting through an npm codex.cmd.
		args = append(args, "-c", "model_reasoning_effort="+in.Effort)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(prompt) // prompt via stdin: no argv length / escaping limits
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Response{}, &Error{Code: CodeTimeout, Provider: "codex", Message: "codex exec did not complete: " + ctxErr.Error(), Retryable: true, Cause: ctxErr}
		}
		msg := truncate(stderr.String(), 500)
		if cliRateLimitRE.MatchString(stderr.String()) {
			return Response{}, &Error{Code: CodeRateLimit, Provider: "codex", Message: msg, Retryable: true, Cause: runErr}
		}
		return Response{}, &Error{Code: CodeServerError, Provider: "codex", Message: "codex exec failed: " + msg, Retryable: true, Cause: runErr}
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "codex", Message: "read codex output: " + err.Error(), Retryable: true, Cause: err}
	}
	out := string(data)
	lines, err := ParseResponse(out, in.Lines, "codex")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: out}, nil
}

// codexCatalog is the subset of `codex debug models` output we read.
type codexCatalog struct {
	Models []struct {
		Slug                     string `json:"slug"`
		DisplayName              string `json:"display_name"`
		Visibility               string `json:"visibility"`
		Priority                 int    `json:"priority"`
		DefaultReasoningLevel    string `json:"default_reasoning_level"`
		SupportedReasoningLevels []struct {
			Effort string `json:"effort"`
		} `json:"supported_reasoning_levels"`
	} `json:"models"`
}

// parseCodexModels keeps the models codex itself lists in its picker
// (visibility "list"), in codex's own priority order.
func parseCodexModels(raw []byte) ([]ModelInfo, error) {
	var cat codexCatalog
	if err := json.Unmarshal(raw, &cat); err != nil {
		return nil, &Error{Code: CodeParseFailed, Provider: "codex", Message: "decode model catalog: " + err.Error(), Cause: err}
	}
	sort.SliceStable(cat.Models, func(i, j int) bool { return cat.Models[i].Priority < cat.Models[j].Priority })
	out := make([]ModelInfo, 0, len(cat.Models))
	for _, m := range cat.Models {
		if m.Visibility != "list" {
			continue
		}
		info := ModelInfo{ID: m.Slug, Label: m.DisplayName, DefaultEffort: m.DefaultReasoningLevel}
		for _, level := range m.SupportedReasoningLevels {
			info.Efforts = append(info.Efforts, level.Effort)
		}
		out = append(out, info)
	}
	return out, nil
}

// ListModels asks codex for the account's current model catalog; `codex debug
// models` refreshes it from the server unless --bundled is passed.
func (p *codexProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	bin, err := exec.LookPath(p.opts.Bin)
	if err != nil {
		return nil, &Error{
			Code: CodeMissingConfig, Provider: "codex",
			Message: "codex CLI not found on PATH: " + p.opts.Bin, Cause: err,
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "debug", "models")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if runErr := cmd.Run(); runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &Error{Code: CodeTimeout, Provider: "codex", Message: "codex debug models did not complete: " + ctxErr.Error(), Cause: ctxErr}
		}
		return nil, &Error{Code: CodeServerError, Provider: "codex", Message: "codex debug models failed: " + truncate(stderr.String(), 500), Cause: runErr}
	}
	models, err := parseCodexModels(stdout.Bytes())
	if err == nil && len(models) == 0 {
		// An empty picker is never a real answer; treating it as one would
		// reset the user's remembered model to the CLI default.
		err = &Error{Code: CodeServerError, Provider: "codex", Message: "codex debug models listed no visible models"}
	}
	return models, err
}
