package provider

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const systemPromptTemplate = `You are a professional subtitle translator.
Translate the following subtitles from %s to %s.

Rules:
- Preserve the [N] prefix exactly as-is.
- Output exactly one translated line per input line, in the same order.
- Do not add explanations, headers, or any text outside the [N] format.
- Proper nouns may be kept in their original form when natural.`

// BuildPrompt produces the system prompt and user prompt for a translation request.
func BuildPrompt(in Request) (system, user string) {
	src := in.SourceLang
	if src == "" {
		src = "the source language"
	}
	system = fmt.Sprintf(systemPromptTemplate, src, in.TargetLang)

	var b strings.Builder
	for _, line := range in.Lines {
		fmt.Fprintf(&b, "[%d] %s\n", line.Index, line.Text)
	}
	user = strings.TrimRight(b.String(), "\n")
	return
}

var indexedLineRE = regexp.MustCompile(`\[(\d+)\]\s*(.+)`)

// ParseResponse extracts [N] text lines from raw and matches them to expected indices.
// Returns PARSE_FAILED if any expected index is missing or empty.
func ParseResponse(raw string, expected []Line, providerName string) ([]TranslatedLine, error) {
	parsed := make(map[int]string, len(expected))
	for _, line := range strings.Split(raw, "\n") {
		m := indexedLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		parsed[idx] = strings.TrimSpace(m[2])
	}

	out := make([]TranslatedLine, 0, len(expected))
	missing := make([]int, 0)
	for _, l := range expected {
		text, ok := parsed[l.Index]
		if !ok || text == "" {
			missing = append(missing, l.Index)
			continue
		}
		out = append(out, TranslatedLine{Index: l.Index, Text: text})
	}
	if len(missing) > 0 {
		return nil, &Error{
			Code:      CodeParseFailed,
			Provider:  providerName,
			Message:   fmt.Sprintf("missing %d indices: %v", len(missing), missing),
			Retryable: true,
		}
	}
	return out, nil
}
