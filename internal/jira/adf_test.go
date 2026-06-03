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
