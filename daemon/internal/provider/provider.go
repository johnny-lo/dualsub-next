// Package provider defines the LLM translation provider interface and
// implementations for OpenAI, Google Gemini, Ollama, and a Claude stub.
package provider

import (
	"context"
	"errors"
	"fmt"
)

type Provider interface {
	Name() string
	Translate(ctx context.Context, in Request) (Response, error)
}

type Request struct {
	Lines      []Line
	SourceLang string
	TargetLang string
	Model      string // empty → provider default
}

type Line struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

type Response struct {
	Lines []TranslatedLine
	Raw   string // raw model output, kept for debugging
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
		return fmt.Sprintf("%s/%s: %s (cause: %v)", e.Provider, e.Code, e.Message, e.Cause)
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
