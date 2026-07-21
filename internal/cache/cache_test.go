package cache

import (
	"context"
	"testing"
	"time"
)

// fakeEmbedder is a deterministic, offline embedder: identical text always maps
// to the same vector. Vectors are chosen so their cosine similarities are known
// exactly, letting the tests assert hit/miss decisions without a live API.
type fakeEmbedder struct {
	vecs map[string][]float32
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if v, ok := f.vecs[text]; ok {
		return v, nil
	}
	// Unknown text embeds to the zero vector, which has cosine 0 with everything.
	return []float32{0, 0, 0}, nil
}

func newFake() *fakeEmbedder {
	return &fakeEmbedder{vecs: map[string][]float32{
		"base":    {1, 0, 0},        // reference
		"same":    {1, 0, 0},        // cosine 1.00 vs base -> identical
		"similar": {0.99, 0.141, 0}, // cosine ~0.99 vs base -> above threshold
		"below":   {0.9, 0.4359, 0}, // cosine 0.90 vs base -> below 0.95 threshold
		"other":   {0, 1, 0},        // cosine 0.00 vs base -> dissimilar
		"axisX":   {1, 0, 0},
		"axisY":   {0, 1, 0},
		"axisZ":   {0, 0, 1},
	}}
}

const threshold = 0.95

func TestIdenticalPromptHits(t *testing.T) {
	c := New(newFake(), threshold, time.Hour, 0)
	if err := c.Store(context.Background(), "base", "PARIS"); err != nil {
		t.Fatal(err)
	}

	got, hit, err := c.Lookup(context.Background(), "same")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("identical prompt did not hit")
	}
	if got != "PARIS" {
		t.Errorf("completion = %q, want PARIS", got)
	}
	if c.Hits() != 1 || c.Misses() != 0 {
		t.Errorf("hits=%d misses=%d, want 1/0", c.Hits(), c.Misses())
	}
}

func TestSimilarPromptHits(t *testing.T) {
	c := New(newFake(), threshold, time.Hour, 0)
	c.Store(context.Background(), "base", "PARIS")

	_, hit, err := c.Lookup(context.Background(), "similar")
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Error("similar prompt (cosine ~0.99) should hit at threshold 0.95")
	}
}

func TestDissimilarPromptMisses(t *testing.T) {
	c := New(newFake(), threshold, time.Hour, 0)
	c.Store(context.Background(), "base", "PARIS")

	_, hit, err := c.Lookup(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("dissimilar prompt (cosine 0) must miss")
	}
	if c.Misses() != 1 {
		t.Errorf("misses = %d, want 1", c.Misses())
	}
}

func TestBelowThresholdMisses(t *testing.T) {
	c := New(newFake(), threshold, time.Hour, 0)
	c.Store(context.Background(), "base", "PARIS")

	_, hit, err := c.Lookup(context.Background(), "below")
	if err != nil {
		t.Fatal(err)
	}
	if hit {
		t.Error("prompt with cosine 0.90 must miss at threshold 0.95")
	}
}

func TestTTLExpiryMisses(t *testing.T) {
	c := New(newFake(), threshold, time.Minute, 0)

	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }
	c.Store(context.Background(), "base", "PARIS")

	// Within TTL: hit.
	if _, hit, _ := c.Lookup(context.Background(), "same"); !hit {
		t.Fatal("expected hit before TTL expiry")
	}

	// Advance past the TTL: the entry is now stale.
	now = now.Add(2 * time.Minute)
	if _, hit, _ := c.Lookup(context.Background(), "same"); hit {
		t.Error("expected miss after TTL expiry")
	}
}

func TestEvictionDropsOldest(t *testing.T) {
	c := New(newFake(), threshold, time.Hour, 2)

	// Three mutually orthogonal entries; max is 2, so the first is evicted.
	c.Store(context.Background(), "axisX", "X")
	c.Store(context.Background(), "axisY", "Y")
	c.Store(context.Background(), "axisZ", "Z")

	if _, hit, _ := c.Lookup(context.Background(), "axisX"); hit {
		t.Error("oldest entry (axisX) should have been evicted")
	}
	if got, hit, _ := c.Lookup(context.Background(), "axisY"); !hit || got != "Y" {
		t.Errorf("axisY lookup: got=%q hit=%v, want Y/true", got, hit)
	}
	if got, hit, _ := c.Lookup(context.Background(), "axisZ"); !hit || got != "Z" {
		t.Errorf("axisZ lookup: got=%q hit=%v, want Z/true", got, hit)
	}
}
