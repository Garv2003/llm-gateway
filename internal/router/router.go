// Package router chooses which registered model should serve a request when
// the client does not pin one explicitly. Selection is driven by a difficulty
// Classifier that scores the prompt; the score maps to a cost tier and the
// cheapest model in that tier is used. A Fallback to the next higher tier is
// offered for retrying failed upstream calls.
package router

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/Garv2003/llm-gateway/internal/registry"
)

// Prompt is the classifier's view of an incoming request.
type Prompt struct {
	Text     string
	Messages int
	JSONMode bool
}

// Classifier scores how hard a prompt is on a 0.0 (trivial) .. 1.0 (hard) scale.
// Implementations may be heuristic or, later, embedding-based; the router only
// depends on this interface.
type Classifier interface {
	Difficulty(p Prompt) float64
}

// Explainer is an optional interface a Classifier can implement to describe the
// factors behind its score, used for decision logging.
type Explainer interface {
	Explain(p Prompt) []string
}

// Decision is the outcome of routing a prompt.
type Decision struct {
	Model   registry.Model
	Tier    string
	Score   float64
	Reasons []string
}

// Router selects models from a registry using a Classifier.
type Router struct {
	reg *registry.Registry
	clf Classifier
}

// New builds a Router. A nil Classifier defaults to the heuristic one.
func New(reg *registry.Registry, clf Classifier) *Router {
	if clf == nil {
		clf = HeuristicClassifier{}
	}
	return &Router{reg: reg, clf: clf}
}

var tierRanks = map[string]int{
	"cheap":    0,
	"standard": 1,
	"premium":  2,
}

func tierRank(tier string) int {
	if r, ok := tierRanks[tier]; ok {
		return r
	}
	return tierRanks["standard"]
}

func tierForScore(s float64) string {
	switch {
	case s < 0.34:
		return "cheap"
	case s < 0.67:
		return "standard"
	default:
		return "premium"
	}
}

// Route picks a model for the prompt. It returns false when the registry has no
// models to choose from.
func (r *Router) Route(p Prompt) (Decision, bool) {
	score := r.clf.Difficulty(p)
	tier := tierForScore(score)

	m, chosenTier, ok := r.pick(tier)
	if !ok {
		return Decision{}, false
	}

	var reasons []string
	if e, ok := r.clf.(Explainer); ok {
		reasons = e.Explain(p)
	}

	return Decision{Model: m, Tier: chosenTier, Score: score, Reasons: reasons}, true
}

// Fallback returns the cheapest model in the next higher cost tier above the
// current model, for retrying after an upstream failure. It returns false when
// no higher tier exists in the registry.
func (r *Router) Fallback(current registry.Model) (registry.Model, bool) {
	cr := tierRank(current.CostTier)

	var candidates []registry.Model
	bestRank := -1
	for _, m := range r.reg.Models() {
		if m.Name == current.Name {
			continue
		}
		mr := tierRank(m.CostTier)
		if mr <= cr {
			continue
		}
		if bestRank == -1 || mr < bestRank {
			bestRank = mr
			candidates = candidates[:0]
		}
		if mr == bestRank {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return registry.Model{}, false
	}
	return cheapest(candidates), true
}

// pick returns the model whose tier is closest to the desired tier. Ties prefer
// the higher tier, then the cheaper model. It returns the tier actually chosen.
func (r *Router) pick(desired string) (registry.Model, string, bool) {
	models := r.reg.Models()
	if len(models) == 0 {
		return registry.Model{}, "", false
	}
	dr := tierRank(desired)

	sort.Slice(models, func(i, j int) bool {
		di := abs(tierRank(models[i].CostTier) - dr)
		dj := abs(tierRank(models[j].CostTier) - dr)
		if di != dj {
			return di < dj
		}
		ri, rj := tierRank(models[i].CostTier), tierRank(models[j].CostTier)
		if ri != rj {
			return ri > rj
		}
		ci, cj := cost(models[i]), cost(models[j])
		if ci != cj {
			return ci < cj
		}
		return models[i].Name < models[j].Name
	})

	return models[0], models[0].CostTier, true
}

func cheapest(models []registry.Model) registry.Model {
	sort.Slice(models, func(i, j int) bool {
		ci, cj := cost(models[i]), cost(models[j])
		if ci != cj {
			return ci < cj
		}
		return models[i].Name < models[j].Name
	})
	return models[0]
}

func cost(m registry.Model) float64 {
	return m.InputCostPer1K + m.OutputCostPer1K
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// ExtractPrompt parses an OpenAI-style chat completion body into a Prompt. It is
// lenient: unparseable bodies yield a zero-value Prompt.
func ExtractPrompt(body []byte) Prompt {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return Prompt{}
	}

	var b strings.Builder
	for _, m := range req.Messages {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			b.WriteString(s)
		} else {
			b.Write(m.Content)
		}
		b.WriteByte('\n')
	}

	p := Prompt{
		Text:     b.String(),
		Messages: len(req.Messages),
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type != "" && req.ResponseFormat.Type != "text" {
		p.JSONMode = true
	}
	return p
}
