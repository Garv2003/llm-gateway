package router

import (
	"fmt"
	"strings"
)

// reasoningKeywords signal a prompt likely needs a stronger model.
var reasoningKeywords = []string{
	"prove", "step by step", "step-by-step", "analyze", "analyse", "debug",
	"reason", "explain why", "derive", "optimize", "optimise", "algorithm",
	"trade-off", "tradeoff", "evaluate", "refactor", "why does", "walk through",
}

// jsonKeywords signal a structured-output request.
var jsonKeywords = []string{"json", "yaml", "schema", "structured output"}

// HeuristicClassifier scores difficulty from cheap, explainable signals: prompt
// length, code fences, structured-output requests, reasoning keywords, and the
// number of messages. It implements Classifier and Explainer.
type HeuristicClassifier struct{}

func (HeuristicClassifier) Difficulty(p Prompt) float64 {
	score, _ := heuristicScore(p)
	return score
}

func (HeuristicClassifier) Explain(p Prompt) []string {
	_, reasons := heuristicScore(p)
	return reasons
}

func heuristicScore(p Prompt) (float64, []string) {
	var score float64
	var reasons []string

	lower := strings.ToLower(p.Text)

	// Length: saturates at ~1500 chars, contributing up to 0.35.
	lengthScore := 0.35 * min1(float64(len(p.Text))/1500.0)
	score += lengthScore
	if lengthScore > 0.01 {
		reasons = append(reasons, fmt.Sprintf("length=%dch", len(p.Text)))
	}

	if strings.Contains(p.Text, "```") {
		score += 0.25
		reasons = append(reasons, "code-fence")
	}

	if p.JSONMode || containsAny(lower, jsonKeywords) {
		score += 0.15
		reasons = append(reasons, "structured-output")
	}

	if kw := firstMatch(lower, reasoningKeywords); kw != "" {
		score += 0.25
		reasons = append(reasons, "reasoning:"+kw)
	}

	if p.Messages > 1 {
		msgScore := 0.15 * min1(float64(p.Messages)/10.0)
		score += msgScore
		reasons = append(reasons, fmt.Sprintf("messages=%d", p.Messages))
	}

	return min1(score), reasons
}

func min1(f float64) float64 {
	if f > 1.0 {
		return 1.0
	}
	return f
}

func containsAny(s string, subs []string) bool {
	return firstMatch(s, subs) != ""
}

func firstMatch(s string, subs []string) string {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return sub
		}
	}
	return ""
}
