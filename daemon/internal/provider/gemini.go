package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type GeminiOptions struct {
	APIKey       string
	BaseURL      string // default: https://generativelanguage.googleapis.com/v1beta
	DefaultModel string // default: gemini-2.5-flash
	Timeout      time.Duration
}

type geminiProvider struct {
	opts GeminiOptions
	http *httpClient
}

func NewGemini(opts GeminiOptions) Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	if opts.DefaultModel == "" {
		opts.DefaultModel = "gemini-2.5-flash"
	}
	return &geminiProvider{
		opts: opts,
		http: newHTTPClient("gemini", opts.Timeout),
	}
}

func (p *geminiProvider) Name() string { return "gemini" }

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature float64 `json:"temperature"`
}

type geminiRequest struct {
	Contents          []geminiContent        `json:"contents"`
	SystemInstruction *geminiContent         `json:"systemInstruction,omitempty"`
	GenerationConfig  geminiGenerationConfig `json:"generationConfig"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
}

func (p *geminiProvider) Translate(ctx context.Context, in Request) (Response, error) {
	if p.opts.APIKey == "" {
		return Response{}, &Error{Code: CodeMissingConfig, Provider: "gemini", Message: "API key not set"}
	}
	model := in.Model
	if model == "" {
		model = p.opts.DefaultModel
	}
	system, user := BuildPrompt(in)

	body := geminiRequest{
		SystemInstruction: &geminiContent{Parts: []geminiPart{{Text: system}}},
		Contents: []geminiContent{
			{Role: "user", Parts: []geminiPart{{Text: user}}},
		},
		GenerationConfig: geminiGenerationConfig{Temperature: 0.3},
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent?key=%s",
		p.opts.BaseURL, url.PathEscape(model), url.QueryEscape(p.opts.APIKey))

	raw, err := p.http.postJSON(ctx, endpoint, nil, body)
	if err != nil {
		return Response{}, err
	}

	var parsed geminiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "gemini", Message: fmt.Sprintf("decode response: %v", err), Cause: err}
	}
	if len(parsed.Candidates) == 0 || len(parsed.Candidates[0].Content.Parts) == 0 {
		return Response{}, &Error{Code: CodeParseFailed, Provider: "gemini", Message: "no candidates in response"}
	}

	var content strings.Builder
	for _, part := range parsed.Candidates[0].Content.Parts {
		content.WriteString(part.Text)
	}
	combined := content.String()

	lines, err := ParseResponse(combined, in.Lines, "gemini")
	if err != nil {
		return Response{}, err
	}
	return Response{Lines: lines, Raw: combined}, nil
}
