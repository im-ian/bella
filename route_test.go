package main

import "testing"

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"flaky test 고쳐":     classImplement,
		"이 PR 리뷰해줘":         classReview,
		"why did this fail": classReview,
		"라이브러리 비교해서 조사":     classResearch,
		"버튼 오타만 고쳐":         classQuick,
		"hello":             classImplement,
	}
	for task, want := range cases {
		if got := classify(task); got != want {
			t.Errorf("%q: got %s want %s", task, got, want)
		}
	}
}

func TestDecideBalancesWhenUsageSpreads(t *testing.T) {
	agents := []Agent{
		agent("claude", 80, map[string]float64{classReview: 90}),
		agent("codex", 30, map[string]float64{classReview: 75}),
		agent("grok", 40, map[string]float64{classReview: 70}),
	}
	got := decide("이 변경 리뷰해줘", agents, defaultConfig())
	if got.Policy != policyBalance {
		t.Fatalf("policy %s", got.Policy)
	}
	if got.Pick != "codex" {
		t.Fatalf("pick %s scores %+v", got.Pick, got.Scores)
	}
}

func TestSpentAgentDoesNotForceBalance(t *testing.T) {
	agents := []Agent{
		agent("claude", 46, map[string]float64{classReview: 90}),
		agent("codex", 37, map[string]float64{classReview: 75}),
		agent("grok", 97, map[string]float64{classReview: 70}),
	}
	got := decide("이 변경 리뷰해줘", agents, defaultConfig())
	if got.Policy != policyBest {
		t.Fatalf("policy %s spread %v", got.Policy, got.Spread)
	}
	if got.Pick != "claude" {
		t.Fatalf("pick %s scores %+v", got.Pick, got.Scores)
	}
}

func TestDecidePicksFitWhenUsageIsClose(t *testing.T) {
	agents := []Agent{
		agent("claude", 40, map[string]float64{classReview: 90}),
		agent("codex", 42, map[string]float64{classReview: 75}),
		agent("grok", 38, map[string]float64{classReview: 70}),
	}
	got := decide("리뷰해줘", agents, defaultConfig())
	if got.Policy != policyBest {
		t.Fatalf("policy %s spread %v", got.Policy, got.Spread)
	}
	if got.Pick != "claude" {
		t.Fatalf("pick %s", got.Pick)
	}
}

func TestDecideSkipsDisabledAgent(t *testing.T) {
	agents := []Agent{
		agent("claude", 46, map[string]float64{classReview: 90}),
		agent("codex", 37, map[string]float64{classReview: 50}),
	}
	agents[0].Disabled = true
	got := decide("리뷰해줘", agents, defaultConfig())
	if got.Pick != "codex" {
		t.Fatalf("pick %s", got.Pick)
	}
}

func TestDecideReportsWhenNoneEnabled(t *testing.T) {
	agents := []Agent{agent("claude", 10, map[string]float64{classImplement: 80})}
	agents[0].Disabled = true
	got := decide("고쳐", agents, defaultConfig())
	if got.Found || got.Reason != "no agent is enabled" {
		t.Fatalf("%+v", got)
	}
}

func TestDecideUsesOnlyInstalledAgent(t *testing.T) {
	agents := []Agent{{Name: "claude", Bin: "/bin/claude", Fit: map[string]float64{classImplement: 10}}}
	got := decide("고쳐", agents, defaultConfig())
	if !got.Found || got.Pick != "claude" {
		t.Fatalf("%+v", got)
	}
}

func agent(name string, used float64, fit map[string]float64) Agent {
	return Agent{
		Name: name,
		Bin:  "/usr/bin/" + name,
		Fit:  fit,
		Usage: Usage{Weekly: Window{
			Used:  used,
			Known: true,
		}},
	}
}
