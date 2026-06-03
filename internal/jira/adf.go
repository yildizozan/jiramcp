package jira

import "strings"

// TextToADF converts a plain-text string into a minimal Atlassian Document
// Format (ADF) document. Jira Cloud v3 requires ADF for rich-text fields such
// as description; sending a raw string is rejected with a 400.
//
// Each line of the input becomes a paragraph. Blank lines produce empty
// paragraphs (preserving vertical spacing). Returns nil for empty input so the
// caller can omit the field entirely rather than send an invalid empty node.
func TextToADF(text string) map[string]any {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Normalize newlines.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	content := make([]any, 0, len(lines))
	for _, line := range lines {
		para := map[string]any{"type": "paragraph"}
		if line != "" {
			para["content"] = []any{
				map[string]any{"type": "text", "text": line},
			}
		}
		content = append(content, para)
	}

	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": content,
	}
}
