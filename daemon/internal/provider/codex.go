package provider

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"regexp"
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

// codexRateLimitRE detects subscription rate-limit / quota messages on stderr.
var codexRateLimitRE = regexp.MustCompile(`(?i)rate.?limit|quota|usage limit|too many requests|\b429\b`)

func (p *codexProvider) Translate(ctx context.Context, in Request) (Response, error) {
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
	model := in.Model
	if model == "" {
		model = p.opts.Model
	}
	if model != "" {
		args = append(args, "-m", model)
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
		if codexRateLimitRE.MatchString(stderr.String()) {
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
