package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Garv2003/llm-gateway/internal/registry"
)

const fixture = `{
  "providers": [
    {"name": "openai", "baseURL": "https://api.openai.com", "keyEnv": "OPENAI_API_KEY"},
    {"name": "local", "baseURL": "http://localhost:11434", "keyEnv": "LOCAL_API_KEY"}
  ],
  "models": [
    {"name": "llama3.1-8b", "provider": "local", "costTier": "cheap", "inputCostPer1K": 0.0, "outputCostPer1K": 0.0},
    {"name": "gpt-4o-mini", "provider": "openai", "costTier": "cheap", "inputCostPer1K": 0.00015, "outputCostPer1K": 0.0006},
    {"name": "gpt-4.1-mini", "provider": "openai", "costTier": "standard", "inputCostPer1K": 0.0004, "outputCostPer1K": 0.0016},
    {"name": "gpt-4o", "provider": "openai", "costTier": "premium", "inputCostPer1K": 0.005, "outputCostPer1K": 0.015}
  ]
}`

func loadRegistry(t *testing.T, body string) *registry.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	reg, err := registry.Load(path)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

func TestRouteEasyPromptToCheapTier(t *testing.T) {
	r := New(loadRegistry(t, fixture), nil)

	dec, ok := r.Route(Prompt{Text: "hi", Messages: 1})
	if !ok {
		t.Fatal("Route returned no decision")
	}
	if dec.Tier != "cheap" {
		t.Errorf("tier = %q (score %.2f), want cheap", dec.Tier, dec.Score)
	}
	if dec.Model.CostTier != "cheap" {
		t.Errorf("model %q tier = %q, want cheap", dec.Model.Name, dec.Model.CostTier)
	}
}

func TestRouteHardPromptToPremiumTier(t *testing.T) {
	r := New(loadRegistry(t, fixture), nil)

	hard := strings.Repeat("Consider the following distributed system design. ", 60) +
		"\n```go\nfunc solve() {}\n```\n" +
		"Prove step by step why this is correct and analyze the trade-offs."

	dec, ok := r.Route(Prompt{Text: hard, Messages: 6})
	if !ok {
		t.Fatal("Route returned no decision")
	}
	if dec.Tier != "premium" {
		t.Errorf("tier = %q (score %.2f), want premium", dec.Tier, dec.Score)
	}
	if dec.Model.CostTier != "premium" {
		t.Errorf("model %q tier = %q, want premium", dec.Model.Name, dec.Model.CostTier)
	}
	if len(dec.Reasons) == 0 {
		t.Error("expected explanation reasons for the decision")
	}
}

func TestRouteMidPromptToStandardTier(t *testing.T) {
	r := New(loadRegistry(t, fixture), nil)

	// Moderate length plus one strong signal (code fence) should land in the
	// middle band.
	mid := strings.Repeat("please write a small helper function. ", 15) + "\n```\ncode\n```"

	dec, ok := r.Route(Prompt{Text: mid, Messages: 2})
	if !ok {
		t.Fatal("Route returned no decision")
	}
	if dec.Tier != "standard" {
		t.Errorf("tier = %q (score %.2f), want standard", dec.Tier, dec.Score)
	}
}

func TestRouteCheapPicksLowestCost(t *testing.T) {
	r := New(loadRegistry(t, fixture), nil)

	dec, ok := r.Route(Prompt{Text: "hi", Messages: 1})
	if !ok {
		t.Fatal("Route returned no decision")
	}
	// Both cheap models qualify; llama (cost 0) must win over gpt-4o-mini.
	if dec.Model.Name != "llama3.1-8b" {
		t.Errorf("model = %q, want cheapest cheap-tier model llama3.1-8b", dec.Model.Name)
	}
}

func TestFallbackReturnsHigherTier(t *testing.T) {
	r := New(loadRegistry(t, fixture), nil)

	cheap, _ := r.reg.Model("gpt-4o-mini")
	fb, ok := r.Fallback(cheap)
	if !ok {
		t.Fatal("Fallback returned nothing for cheap model")
	}
	if tierRank(fb.CostTier) <= tierRank(cheap.CostTier) {
		t.Errorf("fallback tier %q not higher than %q", fb.CostTier, cheap.CostTier)
	}
	if fb.CostTier != "standard" {
		t.Errorf("fallback tier = %q, want the next tier standard", fb.CostTier)
	}

	std, _ := r.reg.Model("gpt-4.1-mini")
	fb2, ok := r.Fallback(std)
	if !ok || fb2.CostTier != "premium" {
		t.Errorf("fallback of standard = %q (ok=%v), want premium", fb2.CostTier, ok)
	}
}

func TestFallbackAtTopTierFails(t *testing.T) {
	r := New(loadRegistry(t, fixture), nil)

	top, _ := r.reg.Model("gpt-4o")
	if _, ok := r.Fallback(top); ok {
		t.Error("expected no fallback above the premium tier")
	}
}

func TestRouteEmptyRegistryFails(t *testing.T) {
	empty := `{"providers": [{"name": "x", "baseURL": "http://x", "keyEnv": "X"}], "models": []}`
	r := New(loadRegistry(t, empty), nil)
	if _, ok := r.Route(Prompt{Text: "hi"}); ok {
		t.Error("expected Route to fail on empty registry")
	}
}

// stubClassifier lets a test drive the tier deterministically, proving the
// Classifier interface is swappable.
type stubClassifier struct{ score float64 }

func (s stubClassifier) Difficulty(Prompt) float64 { return s.score }

func TestClassifierIsSwappable(t *testing.T) {
	reg := loadRegistry(t, fixture)

	// A classifier that always reports maximum difficulty forces premium,
	// regardless of the (trivial) prompt.
	r := New(reg, stubClassifier{score: 1.0})
	dec, ok := r.Route(Prompt{Text: "hi", Messages: 1})
	if !ok {
		t.Fatal("Route returned no decision")
	}
	if dec.Tier != "premium" {
		t.Errorf("tier = %q, want premium from stub classifier", dec.Tier)
	}
	if len(dec.Reasons) != 0 {
		t.Errorf("stub classifier is not an Explainer; reasons = %v", dec.Reasons)
	}
}

func TestExtractPrompt(t *testing.T) {
	body := `{"model":"auto","messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hello"}],"response_format":{"type":"json_object"}}`
	p := ExtractPrompt([]byte(body))
	if p.Messages != 2 {
		t.Errorf("messages = %d, want 2", p.Messages)
	}
	if !strings.Contains(p.Text, "be terse") || !strings.Contains(p.Text, "hello") {
		t.Errorf("text = %q, want concatenated message content", p.Text)
	}
	if !p.JSONMode {
		t.Error("JSONMode = false, want true for json_object response_format")
	}
}
