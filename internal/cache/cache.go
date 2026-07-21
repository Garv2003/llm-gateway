// Package cache provides a semantic response cache for chat completions.
//
// Instead of matching requests byte-for-byte, prompts are embedded into
// vectors and compared by cosine similarity, so paraphrases of a previously
// seen prompt can reuse its completion. The vector store sits behind the small
// Store interface, letting the default in-memory implementation be swapped for
// Redis, pgvector, or another vector database without touching the cache logic.
package cache

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Embedder turns text into a fixed-length vector for similarity comparison.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Store is the pluggable backend behind SemanticCache. The default is an
// in-memory slice; a Redis- or pgvector-backed implementation can satisfy the
// same interface later.
type Store interface {
	// Add records a prompt vector and its completion, expiring at expiresAt.
	Add(vec []float32, completion string, expiresAt time.Time)
	// Nearest returns the completion of the highest-similarity entry that has
	// not expired as of now, along with its cosine similarity. ok is false when
	// the store holds no live entries.
	Nearest(vec []float32, now time.Time) (completion string, score float64, ok bool)
}

// SemanticCache embeds prompts and looks them up by vector similarity.
type SemanticCache struct {
	embedder  Embedder
	store     Store
	threshold float64
	ttl       time.Duration

	hits   atomic.Int64
	misses atomic.Int64

	// now is overridable in tests to exercise TTL expiry deterministically.
	now func() time.Time
}

// New builds a SemanticCache backed by the default in-memory store. A hit
// requires cosine similarity >= threshold; entries live for ttl and the store
// keeps at most maxEntries (0 = unbounded), evicting the oldest first.
func New(embedder Embedder, threshold float64, ttl time.Duration, maxEntries int) *SemanticCache {
	return &SemanticCache{
		embedder:  embedder,
		store:     &memoryStore{maxEntries: maxEntries},
		threshold: threshold,
		ttl:       ttl,
		now:       time.Now,
	}
}

// Lookup embeds the prompt and returns the best cached completion when its
// similarity meets the threshold and it has not expired.
func (c *SemanticCache) Lookup(ctx context.Context, prompt string) (completion string, hit bool, err error) {
	vec, err := c.embedder.Embed(ctx, prompt)
	if err != nil {
		return "", false, err
	}
	comp, score, ok := c.store.Nearest(vec, c.now())
	if ok && score >= c.threshold {
		c.hits.Add(1)
		return comp, true, nil
	}
	c.misses.Add(1)
	return "", false, nil
}

// Store embeds the prompt and records the prompt->completion pair with a TTL.
func (c *SemanticCache) Store(ctx context.Context, prompt, completion string) error {
	vec, err := c.embedder.Embed(ctx, prompt)
	if err != nil {
		return err
	}
	c.store.Add(vec, completion, c.now().Add(c.ttl))
	return nil
}

// Hits returns the number of lookups served from the cache.
func (c *SemanticCache) Hits() int64 { return c.hits.Load() }

// Misses returns the number of lookups that missed.
func (c *SemanticCache) Misses() int64 { return c.misses.Load() }

// memoryStore is the default in-memory vector store.
type memoryStore struct {
	mu         sync.Mutex
	entries    []memEntry
	maxEntries int
}

type memEntry struct {
	vec        []float32
	completion string
	expiresAt  time.Time
}

func (s *memoryStore) Add(vec []float32, completion string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.entries = append(s.entries, memEntry{vec: vec, completion: completion, expiresAt: expiresAt})
	if s.maxEntries > 0 && len(s.entries) > s.maxEntries {
		drop := len(s.entries) - s.maxEntries
		s.entries = append(s.entries[:0:0], s.entries[drop:]...)
	}
}

func (s *memoryStore) Nearest(vec []float32, now time.Time) (string, float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	best := -1.0
	bestIdx := -1
	for i := range s.entries {
		if now.After(s.entries[i].expiresAt) {
			continue
		}
		if sim := cosine(vec, s.entries[i].vec); sim > best {
			best = sim
			bestIdx = i
		}
	}
	if bestIdx == -1 {
		return "", 0, false
	}
	return s.entries[bestIdx].completion, best, true
}

// cosine returns the cosine similarity of two vectors, or 0 when they are of
// unequal length or either has zero magnitude.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
