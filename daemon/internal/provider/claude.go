package provider

import "context"

// ClaudeOptions is reserved for the future Claude (Anthropic) provider.
// Implementation is intentionally deferred until the user has API credit;
// keeping the interface here lets the orchestrator route to it later without
// code changes elsewhere.
type ClaudeOptions struct {
	APIKey string
}

type claudeProvider struct {
	opts ClaudeOptions
}

func NewClaude(opts ClaudeOptions) Provider {
	return &claudeProvider{opts: opts}
}

func (p *claudeProvider) Name() string { return "claude" }

func (p *claudeProvider) DefaultModel() string { return "" }

func (p *claudeProvider) Translate(_ context.Context, _ Request) (Response, error) {
	return Response{}, &Error{
		Code:     CodeNotImplemented,
		Provider: "claude",
		Message:  "Claude provider is not yet implemented",
	}
}
