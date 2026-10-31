// Package schema defines the moderation taxonomy: category names,
// human-readable definitions, the JSON schema used for guided decoding,
// and the prompt sent to the vision model.
package schema

import "strings"

// Categories is the canonical, ordered taxonomy. The order is stable and
// is used for prompts, JSON schema required fields and API responses.
var Categories = []string{
	"nudity",
	"sexual_content",
	"suggestive",
	"violence",
	"graphic_violence",
	"weapons",
	"drugs",
	"alcohol",
	"tobacco",
	"hate_symbols",
	"harassment",
	"self_harm",
	"gore",
	"child_safety",
	"gambling",
}

// Definitions explains each category to the model. Keep entries short:
// long definitions eat context on every request.
var Definitions = map[string]string{
	"nudity":           "exposed genitalia, buttocks or female breasts; full or partial nudity",
	"sexual_content":   "sexual acts, explicit sexual imagery or pornography",
	"suggestive":       "lingerie, swimwear, sexualized posing or framing without explicit nudity",
	"violence":         "physical fights, assault, people being hurt",
	"graphic_violence": "blood, open wounds, graphic injury",
	"weapons":          "guns, knives, explosives or other weapons",
	"drugs":            "illegal drug use, paraphernalia or trafficking",
	"alcohol":          "alcoholic drinks, drinking or intoxication",
	"tobacco":          "smoking, vaping or tobacco products",
	"hate_symbols":     "swastikas, extremist or hate-group symbols",
	"harassment":       "bullying, threats, degrading or abusive content",
	"self_harm":        "self-injury, suicide or self-harm depiction",
	"gore":             "dismemberment, corpses or extreme bloodshed",
	"child_safety":     "minors in sexual, exploitative or dangerous contexts",
	"gambling":         "casino, betting or gambling activity",
}

// JSONSchema returns the response schema (OpenAI-style JSON schema) that the
// model is constrained to when structured output is enabled.
func JSONSchema() map[string]any {
	props := make(map[string]any, len(Categories))
	for _, c := range Categories {
		props[c] = map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	}
	required := make([]string, len(Categories))
	copy(required, Categories)
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"categories": map[string]any{
				"type":       "object",
				"properties": props,
				"required":   required,
			},
			"verdict": map[string]any{"type": "string", "enum": []string{"safe", "review", "block"}},
			"reason":  map[string]any{"type": "string", "maxLength": 240},
		},
		"required": []string{"categories", "verdict", "reason"},
	}
}

// Prompt builds the moderation instruction for a given media kind
// ("image" or "video").
func Prompt(mediaKind string) string {
	var b strings.Builder
	b.WriteString("You are a strict content moderation classifier. Analyze this ")
	b.WriteString(mediaKind)
	b.WriteString(" and rate EVERY category from 0.0 to 1.0 (1 = clearly present, 0 = absent).\n\nCategories:\n")
	for _, c := range Categories {
		b.WriteString("- ")
		b.WriteString(c)
		if d := Definitions[c]; d != "" {
			b.WriteString(": ")
			b.WriteString(d)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Rate what is actually visible; do not speculate about context that is not shown.\n")
	b.WriteString("- If a minor may be involved in any sexual, exploitative or dangerous context, set child_safety high.\n")
	b.WriteString("- Medical or educational imagery should be rated by what is visible, not by intent.\n")
	b.WriteString("- Return ONLY the JSON object required by the schema.\n")
	return b.String()
}

// Known reports whether name is part of the taxonomy. Case sensitive.
func Known(name string) bool {
	for _, c := range Categories {
		if c == name {
			return true
		}
	}
	return false
}
