package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// AgyOptions configures the Google Antigravity CLI provider. No API key is
// required — auth is handled by the user's existing agy sign-in.
type AgyOptions struct {
	Bin     string        // default "agy"
	Model   string        // optional default → agy --model
	Timeout time.Duration // default 4m
}

// agyMaxPromptChars caps the prompt before launch: agy only takes the prompt
// on argv, and Windows limits a whole command line to 32,767 characters.
const agyMaxPromptChars = 24000

type agyProvider struct {
	opts AgyOptions
}

func NewAgy(opts AgyOptions) Provider {
	if opts.Bin == "" {
		opts.Bin = "agy"
	}
	if opts.Timeout == 0 {
		opts.Timeout = 4 * time.Minute
	}
	return &agyProvider{opts: opts}
}

func (p *agyProvider) Name() string { return "agy" }

func (p *agyProvider) DefaultModel() string { return p.opts.Model }

// agyResult is agy's `--output-format json` print-mode result.
type agyResult struct {
	Status   string `json:"status"`
	Response string `json:"response"`
}

func (p *agyProvider) Translate(ctx context.Context, in Request) (Response, error) {
	system, user := BuildPrompt(in)
	prompt := system + "\n\n" + user
	if len(prompt) > agyMaxPromptChars {
		return Response{}, &Error{
			Code: CodeContextTooLong, Provider: "agy",
			Message: fmt.Sprintf("prompt is %d characters; agy takes it on the command line, limit %d", len(prompt), agyMaxPromptChars),
		}
	}
	model := in.Model
	if model == "" {
		model = p.opts.Model
	}
	if model != "" && !validCLIValue(model) {
		return Response{}, &Error{Code: CodeBadRequest, Provider: "agy", Message: fmt.Sprintf("invalid model %q", model)}
	}
	bin, err := p.lookPath()
	if err != nil {
		return Response{}, err
	}

	// agy treats its working directory as the agent's workspace, so give each
	// call an empty one: tools that need no approval find nothing to read.
	workDir, err := os.MkdirTemp("", "dualsub-agy-*")
	if err != nil {
		return Response{}, &Error{Code: CodeServerError, Provider: "agy", Message: "create work dir: " + err.Error(), Cause: err}
	}
	defer os.RemoveAll(workDir)

	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()

	// No --dangerously-skip-permissions: in print mode agy auto-denies every
	// tool that needs approval (writes, commands). The print timeout sits past
	// ours so our deadline lands first.
	args := []string{"-p", prompt, "--output-format", "json",
		"--print-timeout", fmt.Sprintf("%ds", int(p.opts.Timeout.Seconds())+60)}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctxErr := ctx.Err(); runErr != nil && ctxErr != nil {
		return Response{}, &Error{Code: CodeTimeout, Provider: "agy", Message: "agy did not complete: " + ctxErr.Error(), Retryable: true, Cause: ctxErr}
	}
	var result agyResult
	jsonErr := json.Unmarshal(stdout.Bytes(), &result)
	// Only a non-successful run is scanned for failure messages: a successful
	// translation may itself mention "rate limit" or "429".
	if runErr != nil || jsonErr != nil || result.Status != "SUCCESS" {
		if e := classifyAgyOutput(stdout.String() + "\n" + stderr.String()); e != nil {
			return Response{}, e
		}
	}
	if runErr != nil {
		return Response{}, &Error{Code: CodeServerError, Provider: "agy", Message: "agy failed: " + truncate(stderr.String(), 500), Retryable: true, Cause: runErr}
	}
	if jsonErr != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "agy", Message: "decode agy output: " + truncate(stdout.String(), 200), Retryable: true, Cause: jsonErr}
	}
	if result.Status != "SUCCESS" {
		return Response{}, &Error{Code: CodeServerError, Provider: "agy", Message: fmt.Sprintf("agy status %s: %s", result.Status, truncate(result.Response, 500)), Retryable: true}
	}
	lines, err := ParseResponse(result.Response, in.Lines, "agy")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: result.Response}, nil
}

// classifyAgyOutput maps agy's known failure messages to typed errors. agy
// prints the sign-in notice with exit code 0, so a clean exit is no proof of
// success; only a SUCCESS result is.
func classifyAgyOutput(out string) *Error {
	switch {
	case strings.Contains(out, "Please sign in"):
		return &Error{Code: CodeMissingConfig, Provider: "agy", Message: "agy is not signed in"}
	case strings.Contains(out, "is not recognized"):
		return &Error{Code: CodeBadRequest, Provider: "agy", Message: truncate(strings.TrimSpace(out), 500)}
	case cliRateLimitRE.MatchString(out):
		return &Error{Code: CodeRateLimit, Provider: "agy", Message: truncate(strings.TrimSpace(out), 500), Retryable: true}
	}
	return nil
}

func (p *agyProvider) lookPath() (string, error) {
	bin, err := exec.LookPath(p.opts.Bin)
	if err != nil {
		return "", &Error{
			Code: CodeMissingConfig, Provider: "agy",
			Message: "agy CLI not found on PATH: " + p.opts.Bin, Cause: err,
		}
	}
	// cmd.exe re-parses a batch file's arguments, so untrusted subtitle text on
	// a .cmd/.bat command line could be run as commands ("BatBadBut").
	if runtime.GOOS == "windows" {
		switch strings.ToLower(filepath.Ext(bin)) {
		case ".cmd", ".bat":
			return "", &Error{
				Code: CodeMissingConfig, Provider: "agy",
				Message: "agy resolves to a .cmd/.bat shim (" + bin + "); subtitle text cannot be passed safely on its command line",
			}
		}
	}
	return bin, nil
}

// parseAgyModels reads `agy models` stdout: one `id<TAB>label` per line.
func parseAgyModels(stdout string) []ModelInfo {
	var out []ModelInfo
	for _, line := range strings.Split(stdout, "\n") {
		id, label, ok := strings.Cut(strings.TrimRight(line, "\r"), "\t")
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			continue
		}
		out = append(out, ModelInfo{ID: id, Label: strings.TrimSpace(label)})
	}
	return out
}

// ListModels asks agy which models the signed-in account can use now. agy
// encodes effort in the model name (gemini-…-low/-medium/-high), so no
// separate effort levels are reported.
func (p *agyProvider) ListModels(ctx context.Context) ([]ModelInfo, error) {
	bin, err := p.lookPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "models")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if strings.Contains(stdout.String(), "Please sign in") || strings.Contains(stderr.String(), "Please sign in") {
		return nil, &Error{Code: CodeMissingConfig, Provider: "agy", Message: "agy is not signed in"}
	}
	if runErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, &Error{Code: CodeTimeout, Provider: "agy", Message: "agy models did not complete: " + ctxErr.Error(), Cause: ctxErr}
		}
		return nil, &Error{Code: CodeServerError, Provider: "agy", Message: "agy models failed: " + truncate(stderr.String(), 500), Cause: runErr}
	}
	return parseAgyModels(stdout.String()), nil
}
