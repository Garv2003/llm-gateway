package main

import (
	"math"
	"testing"

	"github.com/Garv2003/llm-gateway/internal/registry"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestPromptCost(t *testing.T) {
	m := registry.Model{InputCostPer1K: 0.005, OutputCostPer1K: 0.015}
	// 2000 input tokens -> 2 * 0.005 = 0.010; 500 output -> 0.5 * 0.015 = 0.0075.
	got := promptCost(m, 2000, 500)
	want := 0.010 + 0.0075
	if !almostEqual(got, want) {
		t.Fatalf("promptCost = %v, want %v", got, want)
	}
}

func TestPromptCostZero(t *testing.T) {
	m := registry.Model{InputCostPer1K: 0, OutputCostPer1K: 0}
	if got := promptCost(m, 1000, 500); got != 0 {
		t.Fatalf("free model cost = %v, want 0", got)
	}
}

func TestTokensFor(t *testing.T) {
	if got := tokensFor("12345678"); got != 2 { // 8 chars / 4
		t.Fatalf("tokensFor(8 chars) = %d, want 2", got)
	}
	if got := tokensFor("ab"); got != 1 { // rounds up from 0 to 1 for non-empty
		t.Fatalf("tokensFor(2 chars) = %d, want 1", got)
	}
	if got := tokensFor(""); got != 0 {
		t.Fatalf("tokensFor(empty) = %d, want 0", got)
	}
}

func TestBaselinePicksMostExpensivePremium(t *testing.T) {
	reg, err := registry.Load("../../models.json")
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	m, err := baselineModel(reg, "")
	if err != nil {
		t.Fatalf("baselineModel: %v", err)
	}
	if m.CostTier != "premium" {
		t.Fatalf("baseline tier = %q, want premium", m.CostTier)
	}
	if m.Name != "gpt-4o" {
		t.Fatalf("baseline model = %q, want gpt-4o", m.Name)
	}
}

func TestBaselineOverride(t *testing.T) {
	reg, err := registry.Load("../../models.json")
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	m, err := baselineModel(reg, "gpt-4.1-mini")
	if err != nil {
		t.Fatalf("baselineModel override: %v", err)
	}
	if m.Name != "gpt-4.1-mini" {
		t.Fatalf("override model = %q, want gpt-4.1-mini", m.Name)
	}
	if _, err := baselineModel(reg, "does-not-exist"); err == nil {
		t.Fatal("expected error for unknown baseline model")
	}
}
