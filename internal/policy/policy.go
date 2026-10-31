// Package policy turns per-category model scores into a final verdict using
// two configurable thresholds (review and block) per category.
package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/codewith-aditya/astra-moderation/internal/schema"
)

// Threshold holds the review/block cutoffs for one category.
type Threshold struct {
	Review float64 `json:"review"`
	Block  float64 `json:"block"`
}

// Flag is one category that crossed the review or block threshold.
type Flag struct {
	Category string  `json:"category"`
	Score    float64 `json:"score"`
	Level    string  `json:"level"` // "review" | "block"
}

// Policy is the complete threshold configuration. Categories override the
// defaults; unknown categories fall back to Default.
type Policy struct {
	Default    Threshold            `json:"default"`
	Categories map[string]Threshold `json:"categories"`
}

// Default returns a sane starting policy. child_safety blocks aggressively;
// sexual/nudity sit slightly lower than other categories.
func Default() Policy {
	return Policy{
		Default: Threshold{Review: 0.40, Block: 0.80},
		Categories: map[string]Threshold{
			"child_safety":     {Review: 0.10, Block: 0.30},
			"sexual_content":   {Review: 0.30, Block: 0.70},
			"nudity":           {Review: 0.30, Block: 0.70},
			"graphic_violence": {Review: 0.35, Block: 0.75},
			"gore":             {Review: 0.35, Block: 0.75},
		},
	}
}

// Load reads a JSON policy file. Missing files are an error; unknown
// categories are rejected to catch typos early.
func Load(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, fmt.Errorf("policy: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(raw, &p); err != nil {
		return Policy{}, fmt.Errorf("policy: parse: %w", err)
	}
	return Normalize(p)
}

// Normalize fills defaults for empty fields and validates categories.
func Normalize(p Policy) (Policy, error) {
	def := Default()
	if p.Default.Review <= 0 && p.Default.Block <= 0 {
		p.Default = def.Default
	}
	if p.Default.Block <= p.Default.Review {
		return Policy{}, errors.New("policy: default block must be greater than review")
	}
	if p.Categories == nil {
		p.Categories = map[string]Threshold{}
	}
	for name, th := range p.Categories {
		if !schema.Known(name) {
			return Policy{}, fmt.Errorf("policy: unknown category %q", name)
		}
		if th.Block <= th.Review {
			return Policy{}, fmt.Errorf("policy: category %q block must be greater than review", name)
		}
	}
	return p, nil
}

// ThresholdFor returns the effective threshold for a category.
func (p Policy) ThresholdFor(category string) Threshold {
	if t, ok := p.Categories[category]; ok {
		return t
	}
	if p.Default.Block <= 0 {
		return Default().Default
	}
	return p.Default
}

// Merge overlays override on base; used for per-tenant policies.
func Merge(base Policy, override *Policy) Policy {
	if override == nil {
		return base
	}
	out := Policy{
		Default:    base.Default,
		Categories: map[string]Threshold{},
	}
	for k, v := range base.Categories {
		out.Categories[k] = v
	}
	if override.Default.Block > 0 {
		out.Default = override.Default
	}
	for k, v := range override.Categories {
		out.Categories[k] = v
	}
	return out
}

// Evaluate applies thresholds and returns the final verdict together with the
// list of flagged categories sorted by score (descending).
func Evaluate(p Policy, scores map[string]float64) (string, []Flag) {
	verdict := "safe"
	flags := make([]Flag, 0, 4)
	for _, c := range schema.Categories {
		score := scores[c]
		t := p.ThresholdFor(c)
		switch {
		case score >= t.Block:
			flags = append(flags, Flag{Category: c, Score: score, Level: "block"})
			verdict = "block"
		case score >= t.Review:
			flags = append(flags, Flag{Category: c, Score: score, Level: "review"})
			if verdict == "safe" {
				verdict = "review"
			}
		}
	}
	sort.Slice(flags, func(i, j int) bool {
		if flags[i].Level != flags[j].Level {
			return flags[i].Level == "block"
		}
		return flags[i].Score > flags[j].Score
	})
	return verdict, flags
}

// FlaggedMap converts a flag list into the OpenAI-style boolean map keyed by
// category (true when the category reached its block threshold).
func FlaggedMap(flags []Flag) map[string]bool {
	out := map[string]bool{}
	for _, f := range flags {
		out[f.Category] = f.Level == "block"
	}
	return out
}
