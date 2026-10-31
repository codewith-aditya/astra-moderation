package policy

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestEvaluate(t *testing.T) {
	p := Default()
	scores := map[string]float64{"nudity": 0.95, "suggestive": 0.5}
	verdict, flags := Evaluate(p, scores)
	if verdict != "block" {
		t.Fatalf("verdict = %q, want block", verdict)
	}
	if len(flags) != 2 {
		t.Fatalf("flags = %d, want 2", len(flags))
	}
	if flags[0].Category != "nudity" || flags[0].Level != "block" {
		t.Fatalf("first flag = %+v", flags[0])
	}
}

func TestEvaluateReviewOnly(t *testing.T) {
	p := Default()
	verdict, flags := Evaluate(p, map[string]float64{"nudity": 0.5})
	if verdict != "review" {
		t.Fatalf("verdict = %q, want review", verdict)
	}
	if len(flags) != 1 || flags[0].Level != "review" {
		t.Fatalf("flags = %+v", flags)
	}
}

func TestChildSafetyAggressiveThreshold(t *testing.T) {
	p := Default()
	verdict, _ := Evaluate(p, map[string]float64{"child_safety": 0.35})
	if verdict != "block" {
		t.Fatalf("child_safety 0.35 verdict = %q, want block", verdict)
	}
}

func TestThresholdFallback(t *testing.T) {
	p := Default()
	th := p.ThresholdFor("gambling")
	if !approx(th.Block, 0.80) || !approx(th.Review, 0.40) {
		t.Fatalf("fallback threshold = %+v", th)
	}
}

func TestMerge(t *testing.T) {
	base := Default()
	override := &Policy{
		Default:    Threshold{Review: 0.1, Block: 0.2},
		Categories: map[string]Threshold{"nudity": {Review: 0.2, Block: 0.5}},
	}
	got := Merge(base, override)
	if !approx(got.ThresholdFor("nudity").Block, 0.5) {
		t.Fatalf("merged nudity block = %v", got.ThresholdFor("nudity").Block)
	}
	if !approx(got.ThresholdFor("gambling").Block, 0.2) {
		t.Fatalf("merged default block = %v", got.ThresholdFor("gambling").Block)
	}
	if !approx(base.ThresholdFor("gambling").Block, 0.8) {
		t.Fatal("Merge mutated the base policy")
	}
}

func TestNormalizeRejectsUnknownCategory(t *testing.T) {
	_, err := Normalize(Policy{
		Default:    Threshold{Review: 0.4, Block: 0.8},
		Categories: map[string]Threshold{"bodily_harm": {Review: 0.3, Block: 0.6}},
	})
	if err == nil {
		t.Fatal("expected error for unknown category")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.json")
	raw := `{"default":{"review":0.3,"block":0.7},"categories":{"nudity":{"review":0.2,"block":0.5}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.ThresholdFor("nudity").Block != 0.5 {
		t.Fatalf("nudity block = %v", p.ThresholdFor("nudity").Block)
	}
}

func TestFlaggedMap(t *testing.T) {
	m := FlaggedMap([]Flag{{Category: "gore", Level: "review"}, {Category: "nudity", Level: "block"}})
	if m["gore"] {
		t.Fatal("review-level flag must not be true")
	}
	if !m["nudity"] {
		t.Fatal("block-level flag must be true")
	}
}
