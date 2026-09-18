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

// ADFToText flattens an Atlassian Document Format document back into plain
// text. It is the counterpart of TextToADF for reading Cloud rich-text fields
// (comment bodies), which the v3 API returns as an ADF tree rather than a
// string.
//
// The walk is deliberately lossy: only the text content is recovered, marks
// (bold, links) and node types we do not model (tables, media) contribute their
// nested text and nothing else. Top-level blocks are separated by newlines and
// hardBreak nodes become newlines, which round-trips the documents TextToADF
// produces.
func ADFToText(doc map[string]any) string {
	content, _ := doc["content"].([]any)
	blocks := make([]string, 0, len(content))
	for _, node := range content {
		blocks = append(blocks, adfNodeText(node))
	}
	return strings.Join(blocks, "\n")
}

// adfNodeText renders a single ADF node and its descendants to text.
func adfNodeText(node any) string {
	n, ok := node.(map[string]any)
	if !ok {
		return ""
	}
	switch n["type"] {
	case "text":
		s, _ := n["text"].(string)
		return s
	case "hardBreak":
		return "\n"
	}
	children, _ := n["content"].([]any)
	var b strings.Builder
	for _, child := range children {
		b.WriteString(adfNodeText(child))
	}
	return b.String()
}
