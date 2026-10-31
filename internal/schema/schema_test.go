package schema

import "testing"

func TestTaxonomy(t *testing.T) {
	if len(Categories) != 15 {
		t.Fatalf("categories = %d, want 15", len(Categories))
	}
	seen := map[string]bool{}
	for _, c := range Categories {
		if seen[c] {
			t.Fatalf("duplicate category %q", c)
		}
		seen[c] = true
		if Definitions[c] == "" {
			t.Fatalf("missing definition for %q", c)
		}
	}
}

func TestJSONSchemaShape(t *testing.T) {
	s := JSONSchema()
	required, _ := s["required"].([]string)
	if len(required) != 3 {
		t.Fatalf("required = %v", required)
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing")
	}
	cats, ok := props["categories"].(map[string]any)
	if !ok {
		t.Fatal("categories missing")
	}
	req, _ := cats["required"].([]string)
	if len(req) != len(Categories) {
		t.Fatalf("category required = %d", len(req))
	}
}

func TestPromptMentionsEverything(t *testing.T) {
	p := Prompt("video")
	for _, c := range Categories {
		if !contains(p, c) {
			t.Fatalf("prompt missing %q", c)
		}
	}
	if !contains(p, "video") {
		t.Fatal("prompt missing media kind")
	}
}

func TestKnown(t *testing.T) {
	if !Known("nudity") {
		t.Fatal("nudity should be known")
	}
	if Known("bodily_harm") {
		t.Fatal("bodily_harm should not be known")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
