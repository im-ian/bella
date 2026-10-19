package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaretSitsAfterTypedText(t *testing.T) {
	view := screen{
		width:  40,
		height: 16,
		mode:   "auto",
		input:  []rune("ab"),
	}
	raw := render(view)
	if !strings.HasSuffix(raw, "\x1b[13;5H\x1b[?25h") {
		t.Fatalf("caret suffix %q", raw[len(raw)-20:])
	}
	korean := view
	korean.input = []rune("리뷰")
	raw = render(korean)
	if !strings.HasSuffix(raw, "\x1b[13;7H\x1b[?25h") {
		t.Fatalf("korean caret suffix %q", raw[len(raw)-20:])
	}
	plain := stripANSI(render(screen{width: 40, height: 16, mode: "auto"}))
	if !strings.Contains(plain, "write a task") {
		t.Fatal(plain)
	}
}

func TestCycleMode(t *testing.T) {
	if cycleMode("auto", 1) != policyBalance || cycleMode(policyBalance, 1) != policyBest || cycleMode(policyBest, 1) != "auto" {
		t.Fatal(cycleMode("auto", 1), cycleMode(policyBalance, 1), cycleMode(policyBest, 1))
	}
	if cycleMode("auto", -1) != policyBest {
		t.Fatal(cycleMode("auto", -1))
	}
}

func TestHomeShowsModeChips(t *testing.T) {
	raw := render(screen{width: 60, height: 20, mode: "auto"})
	plain := stripANSI(raw)
	for _, want := range []string{"auto", "balance", "fit", "tab mode"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q\n%s", want, plain)
		}
	}
	if !strings.Contains(raw, "\x1b[7m auto \x1b[0m") {
		t.Fatal("auto is not the selected chip")
	}
}

func TestRenderHomeUsesComposer(t *testing.T) {
	view := screen{
		width:  60,
		height: 20,
		mode:   "auto",
		agents: []Agent{
			{Name: "claude", Bin: "/bin/claude", Usage: Usage{Weekly: Window{Used: 46, Known: true}}},
			{Name: "grok", Bin: "/bin/grok", Disabled: true, Usage: Usage{Weekly: Window{Used: 97, Known: true}}},
		},
		input: []rune("리뷰"),
	}
	out := stripANSI(render(view))
	for _, want := range []string{"bella", "auto", "claude", "week 46%", "grok", "off", "╭ task", "리뷰", "^S settings"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
}

func TestRenderSettingsSelectsFirstAgent(t *testing.T) {
	view := screen{
		width:    60,
		height:   22,
		settings: true,
		mode:     "auto",
		agents: []Agent{
			{Name: "claude", Bin: "/bin/claude"},
			{Name: "codex", Bin: "/bin/codex", Disabled: true},
			{Name: "grok", Bin: "/bin/grok"},
		},
	}
	raw := render(view)
	plain := stripANSI(raw)
	for _, want := range []string{"settings", "[x]  claude", "[ ]  codex", "(*)  auto", "space toggle"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q\n%s", want, plain)
		}
	}
	if !strings.Contains(raw, "\x1b[7m") {
		t.Fatal("selected row is not reversed")
	}
}

func TestSetAgentPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("BELLA_CONFIG", path)
	cfg := defaultConfig()
	agents := []Agent{{Name: "grok", Bin: "/bin/grok"}}
	if err := setAgent(&cfg, agents, "grok", false); err != nil {
		t.Fatal(err)
	}
	if !agents[0].Disabled {
		t.Fatal("memory not updated")
	}
	loaded := loadConfig()
	if loaded.enabled("grok") || !loaded.enabled("claude") {
		t.Fatalf("%v", loaded.Enabled)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
