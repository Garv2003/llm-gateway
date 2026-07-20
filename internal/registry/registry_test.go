package registry

import (
	"os"
	"path/filepath"
	"testing"
)

const fixture = `{
  "providers": [
    {"name": "openai", "baseURL": "https://api.openai.com", "keyEnv": "OPENAI_API_KEY"},
    {"name": "local", "baseURL": "http://localhost:11434", "keyEnv": "LOCAL_API_KEY"}
  ],
  "models": [
    {"name": "gpt-4o-mini", "provider": "openai", "costTier": "cheap", "inputCostPer1K": 0.00015, "outputCostPer1K": 0.0006, "capabilities": ["chat"]},
    {"name": "gpt-4o", "provider": "openai", "costTier": "premium", "inputCostPer1K": 0.005, "outputCostPer1K": 0.015, "capabilities": ["chat", "vision"]}
  ]
}`

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestLoadAndResolveModel(t *testing.T) {
	reg, err := Load(writeFixture(t, fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	m, ok := reg.Model("gpt-4o-mini")
	if !ok {
		t.Fatal("gpt-4o-mini not found")
	}
	if m.Provider != "openai" {
		t.Errorf("provider = %q, want openai", m.Provider)
	}
	if m.CostTier != "cheap" {
		t.Errorf("costTier = %q, want cheap", m.CostTier)
	}
	if m.InputCostPer1K != 0.00015 {
		t.Errorf("inputCostPer1K = %v", m.InputCostPer1K)
	}

	p, ok := reg.Provider(m.Provider)
	if !ok {
		t.Fatal("provider openai not found")
	}
	if p.BaseURL != "https://api.openai.com" {
		t.Errorf("baseURL = %q", p.BaseURL)
	}
}

func TestResolveMissingModel(t *testing.T) {
	reg, err := Load(writeFixture(t, fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := reg.Model("does-not-exist"); ok {
		t.Fatal("missing model reported as found")
	}
}

func TestResolveKeyFromEnv(t *testing.T) {
	reg, err := Load(writeFixture(t, fixture))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "sk-test-123")
	if got := reg.ResolveKey("openai"); got != "sk-test-123" {
		t.Errorf("ResolveKey(openai) = %q, want sk-test-123", got)
	}
	if got := reg.ResolveKey("nope"); got != "" {
		t.Errorf("ResolveKey(nope) = %q, want empty", got)
	}
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	body := `{"providers": [], "models": [{"name": "m", "provider": "ghost"}]}`
	if _, err := Load(writeFixture(t, body)); err == nil {
		t.Fatal("expected error for model referencing unknown provider")
	}
}
