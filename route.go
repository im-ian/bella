package main

import (
	"fmt"
	"strings"
)

const (
	classImplement = "implement"
	classReview    = "review"
	classResearch  = "research"
	classQuick     = "quick"

	policyBalance = "balance"
	policyBest    = "best"
)

type Agent struct {
	Name     string
	Bin      string
	Usage    Usage
	Fit      map[string]float64
	Args     []string
	Disabled bool
}

type Score struct {
	Name     string
	Value    float64
	Pressure float64
	Known    bool
	Fit      float64
}

type Decision struct {
	Class  string
	Policy string
	Pick   string
	Reason string
	Scores []Score
	Spread float64
	Agent  Agent
	Found  bool
}

func classify(task string) string {
	text := strings.ToLower(task)
	rules := []struct {
		class string
		words []string
	}{
		{classReview, []string{"리뷰", "review", "설명해", "설명", "봐줘", "살펴", "why ", "why", "왜"}},
		{classResearch, []string{"조사", "찾아", "research", "비교", "compare", "알아봐"}},
		{classQuick, []string{"오타", "typo", "rename", "이름만", "한 줄", "짧게"}},
		{classImplement, []string{"구현", "고쳐", "수정", "fix", "버그", "bug", "추가", "refactor", "리팩"}},
	}
	for _, rule := range rules {
		for _, word := range rule.words {
			if strings.Contains(text, strings.ToLower(word)) {
				return rule.class
			}
		}
	}
	return classImplement
}

func decide(task string, agents []Agent, cfg Config) Decision {
	class := classify(task)
	installed := 0
	ready := make([]Agent, 0, len(agents))
	for _, agent := range agents {
		if agent.Bin == "" {
			continue
		}
		installed++
		if agent.Disabled {
			continue
		}
		ready = append(ready, agent)
	}
	if len(ready) == 0 {
		if installed == 0 {
			return Decision{Class: class, Reason: "claude, codex, and grok are not on PATH"}
		}
		return Decision{Class: class, Reason: "no agent is enabled"}
	}

	pressures := make([]float64, 0, len(ready))
	scored := make([]Score, 0, len(ready))
	for _, agent := range ready {
		p, known := pressure(agent.Usage)
		if known {
			pressures = append(pressures, p)
		}
		scored = append(scored, Score{
			Name:     agent.Name,
			Pressure: p,
			Known:    known,
			Fit:      fitOf(agent, class),
		})
	}

	spread := spreadOf(usablePressures(pressures))

	policy := cfg.Mode
	if policy == "" || policy == "auto" {
		if spread >= cfg.BalanceGap {
			policy = policyBalance
		} else {
			policy = policyBest
		}
	}

	bestIdx := 0
	for i := range scored {
		rem := 50.0
		if scored[i].Known {
			rem = 100 - scored[i].Pressure
		}
		scored[i].Value = blend(rem, scored[i].Fit, policy == policyBalance)
		if scored[i].Known && scored[i].Pressure >= 90 {
			scored[i].Value *= 0.2
		}
		if better(scored[i], scored[bestIdx]) {
			bestIdx = i
		}
	}

	pick := ready[bestIdx]
	return Decision{
		Class:  class,
		Policy: policy,
		Pick:   pick.Name,
		Reason: reason(pick, scored[bestIdx], policy, spread, len(ready)),
		Scores: scored,
		Spread: spread,
		Agent:  pick,
		Found:  true,
	}
}

func better(a, b Score) bool {
	if a.Value != b.Value {
		return a.Value > b.Value
	}
	if a.Known != b.Known {
		return a.Known
	}
	if a.Pressure != b.Pressure {
		return a.Pressure < b.Pressure
	}
	return a.Name < b.Name
}

func blend(remaining, fit float64, balance bool) float64 {
	if balance {
		return remaining*0.75 + fit*0.25
	}
	return remaining*0.25 + fit*0.75
}

func fitOf(agent Agent, class string) float64 {
	if agent.Fit == nil {
		return 50
	}
	if v, ok := agent.Fit[class]; ok {
		return v
	}
	return 50
}

func reason(agent Agent, score Score, policy string, spread float64, n int) string {
	if n == 1 {
		return "only " + agent.Name + " is installed, so the task goes there"
	}
	if !score.Known {
		return "usage is missing, so this goes to " + agent.Name
	}
	if policy == policyBalance {
		return fmt.Sprintf("usage is %.0f points apart, so %s gets it. usage %.0f%%", spread, agent.Name, score.Pressure)
	}
	return fmt.Sprintf("usage is close, so %s gets it. usage %.0f%%", agent.Name, score.Pressure)
}

// usablePressures ignores a spent subscription when at least two others still
// have room. One full account should not force every task onto the least-used
// of the rest.
func usablePressures(pressures []float64) []float64 {
	open := make([]float64, 0, len(pressures))
	for _, p := range pressures {
		if p < 90 {
			open = append(open, p)
		}
	}
	if len(open) >= 2 {
		return open
	}
	return pressures
}

func spreadOf(pressures []float64) float64 {
	if len(pressures) < 2 {
		return 0
	}
	minP, maxP := pressures[0], pressures[0]
	for _, p := range pressures[1:] {
		if p < minP {
			minP = p
		}
		if p > maxP {
			maxP = p
		}
	}
	return maxP - minP
}

func classLabel(class string) string {
	switch class {
	case classReview:
		return "review"
	case classResearch:
		return "research"
	case classQuick:
		return "quick"
	default:
		return "implement"
	}
}

func policyLabel(policy string) string {
	return modeLabel(policy)
}

func modeLabel(mode string) string {
	switch mode {
	case policyBalance:
		return "balance"
	case policyBest:
		return "fit"
	default:
		return "auto"
	}
}
