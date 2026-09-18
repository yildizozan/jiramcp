package jira

import (
	"encoding/json"
	"testing"
)

func TestTextToADF_Empty(t *testing.T) {
	if got := TextToADF("   "); got != nil {
		t.Fatalf("expected nil for blank text, got %v", got)
	}
}

func TestTextToADF_Structure(t *testing.T) {
	doc := TextToADF("hello\n\nworld")
	if doc["type"] != "doc" || doc["version"] != 1 {
		t.Fatalf("bad doc envelope: %v", doc)
	}
	content, ok := doc["content"].([]any)
	if !ok || len(content) != 3 {
		t.Fatalf("expected 3 paragraphs, got %v", doc["content"])
	}
	// First paragraph has a text node; middle (blank) has none.
	first := content[0].(map[string]any)
	if _, ok := first["content"]; !ok {
		t.Fatalf("first paragraph should have content")
	}
	mid := content[1].(map[string]any)
	if _, ok := mid["content"]; ok {
		t.Fatalf("blank line should produce an empty paragraph")
	}

	// Must round-trip as valid JSON.
	if _, err := json.Marshal(doc); err != nil {
		t.Fatalf("adf not json-serializable: %v", err)
	}
}

func TestADFToText_RoundTrip(t *testing.T) {
	const text = "first line\n\nthird line"
	if got := ADFToText(TextToADF(text)); got != text {
		t.Fatalf("round trip lost content: %q != %q", got, text)
	}
}

func TestADFToText_NestedAndUnknownNodes(t *testing.T) {
	doc := map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{"type": "paragraph", "content": []any{
				map[string]any{"type": "text", "text": "bold", "marks": []any{map[string]any{"type": "strong"}}},
				map[string]any{"type": "hardBreak"},
				map[string]any{"type": "text", "text": "after break"},
			}},
			// A node type we do not model still contributes its nested text.
			map[string]any{"type": "bulletList", "content": []any{
				map[string]any{"type": "listItem", "content": []any{
					map[string]any{"type": "paragraph", "content": []any{
						map[string]any{"type": "text", "text": "item"},
					}},
				}},
			}},
		},
	}
	want := "bold\nafter break\nitem"
	if got := ADFToText(doc); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestADFToText_Malformed(t *testing.T) {
	if got := ADFToText(map[string]any{"type": "doc"}); got != "" {
		t.Fatalf("expected empty string for a doc with no content, got %q", got)
	}
}
