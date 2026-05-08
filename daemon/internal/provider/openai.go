package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type OpenAIOptions struct {
	APIKey       string
	BaseURL      string // default: https://api.openai.com/v1
	DefaultModel string // default: gpt-4o-mini
	Timeout      time.Duration
}

type openaiProvider struct {
	opts OpenAIOptions
	http *httpClient
}

func NewOpenAI(opts OpenAIOptions) Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://api.openai.com/v1"
	}
	if opts.DefaultModel == "" {
		opts.DefaultModel = "gpt-4o-mini"
	}
	return &openaiProvider{
		opts: opts,
		http: newHTTPClient("openai", opts.Timeout),
	}
}

func (p *openaiProvider) Name() string { return "openai" }

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Temperature float64         `json:"temperature"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (p *openaiProvider) Translate(ctx context.Context, in Request) (Response, error) {
	if p.opts.APIKey == "" {
		return Response{}, &Error{Code: CodeMissingConfig, Provider: "openai", Message: "API key not set"}
	}
	model := in.Model
	if model == "" {
		model = p.opts.DefaultModel
	}
	system, user := BuildPrompt(in)

	body := openaiRequest{
		Model: model,
		Messages: []openaiMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0.3,
	}
	headers := map[string]string{"Authorization": "Bearer " + p.opts.APIKey}

	raw, err := p.http.postJSON(ctx, p.opts.BaseURL+"/chat/completions", headers, body)
	if err != nil {
		return Response{}, err
	}

	var parsed openaiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "openai", Message: fmt.Sprintf("decode response: %v", err), Cause: err}
	}
	if len(parsed.Choices) == 0 {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "openai", Message: "no choices in response"}
	}
	content := parsed.Choices[0].Message.Content
	lines, err := ParseResponse(content, in.Lines, "openai")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: content}, nil
}
