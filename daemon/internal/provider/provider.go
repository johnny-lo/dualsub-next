// Package provider defines the LLM translation provider interface and
// implementations for OpenAI, Google Gemini, Ollama, and a Claude stub.
package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

type Provider interface {
	Name() string
	DefaultModel() string
	Translate(ctx context.Context, in Request) (Response, error)
}

type Request struct {
	Lines      []Line
	SourceLang string
	TargetLang string
	Model      string // empty → provider default
	Effort     string // empty → provider/CLI default; only codex uses it
}

// ModelInfo is one model a provider's account can use right now.
type ModelInfo struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
}

// ModelLister is implemented by providers that can ask their API or CLI which
// models are available, so nothing has to hardcode a model list.
type ModelLister interface {
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// ErrListUnsupported is returned by wrappers whose provider cannot list models.
var ErrListUnsupported = errors.New("provider cannot list models")

// cliValueRE bounds model and effort values handed to a CLI on its argv. They
// arrive over HTTP, and on Windows an npm `.cmd` shim runs through cmd.exe.
var cliValueRE = regexp.MustCompile(`^[A-Za-z0-9._:/-]+$`)

func validCLIValue(s string) bool { return cliValueRE.MatchString(s) }

type Line struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type Response struct {
	Lines        []TranslatedLine
	Raw          string // raw model output, kept for debugging
	QueueForSync bool   // translated locally after the shared cache was unavailable
}

type TranslatedLine struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

const (
	CodeRateLimit      = "PROVIDER_RATE_LIMIT"
	CodeInvalidKey     = "PROVIDER_INVALID_KEY"
	CodeTimeout        = "PROVIDER_TIMEOUT"
	CodeServerError    = "PROVIDER_SERVER_ERROR"
	CodeBadRequest     = "PROVIDER_BAD_REQUEST"
	CodeContextTooLong = "PROVIDER_CONTEXT_TOO_LONG"
	CodeParseFailed    = "PARSE_FAILED"
	CodeNetwork        = "NETWORK"
	CodeNotImplemented = "NOT_IMPLEMENTED"
	CodeMissingConfig  = "MISSING_CONFIG"
)

type Error struct {
	Code      string
	Message   string
	Provider  string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return sanitizeMessage(fmt.Sprintf("%s/%s: %s (cause: %v)", e.Provider, e.Code, e.Message, e.Cause))
	}
	return fmt.Sprintf("%s/%s: %s", e.Provider, e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func IsRetryable(err error) bool {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return false
}
