package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaretSitsAfterTypedText(t *testing.T) {
	view := screen{
		width:  40,
		height: 16,
		mode:   "auto",
		input:  []rune("ab"),
	}
	raw := render(view)
	if !strings.HasSuffix(raw, "\x1b[13;5H\x1b[?25h\x1b[?2026l") {
		t.Fatalf("caret suffix %q", raw[len(raw)-30:])
	}
	rows := frameRows(raw)
	prompt := lineWith(rows, "> ab")
	at := strings.Index(strings.Join(rows, "\n"), prompt)
	rule := strings.Repeat("─", view.width)
	if prompt != "> ab" || at < 0 || !strings.Contains(strings.Join(rows, "\n"), rule+"\n> ab\n"+rule) {
		t.Fatalf("prompt rules\n%q", rows)
	}
	korean := view
	korean.input = []rune("리뷰")
	raw = render(korean)
	if !strings.HasSuffix(raw, "\x1b[13;7H\x1b[?25h\x1b[?2026l") {
		t.Fatalf("korean caret suffix %q", raw[len(raw)-30:])
	}
	plain := stripANSI(render(screen{width: 40, height: 16, mode: "auto"}))
	if strings.Contains(plain, "Enter a task") || strings.Contains(plain, "│") || !strings.Contains(plain, "> ") {
		t.Fatal(plain)
	}
}

func TestFrameRedrawsInPlace(t *testing.T) {
	raw := render(screen{width: 40, height: 16, mode: "auto"})
	if !strings.HasPrefix(raw, "\x1b[?2026h\x1b[?1049h\x1b[?7l\x1b[?25l\x1b[1;1H\x1b[2K") {
		t.Fatalf("frame prefix %q", raw[:48])
	}
	run := &liveRun{
		agent: "claude",
		done:  true,
		lines: []transcriptLine{{kind: "text", text: "hello\x1b[2J\x1b[Hthere"}},
	}
	raw = render(screen{width: 40, height: 16, live: run})
	if strings.Contains(raw, "\x1b[2J") {
		t.Fatal("transcript cleared the screen")
	}
	if !strings.Contains(raw, "hellothere") {
		t.Fatal(raw)
	}
}

func TestWordmarkTickLeavesTheCursor(t *testing.T) {
	patch := wordmarkPatch(2, 13, 5)
	if !strings.HasPrefix(patch, "\x1b[?2026h") || !strings.HasSuffix(patch, "\x1b[?2026l") {
		t.Fatal(patch)
	}
	if !strings.Contains(patch, "\x1b[13;5H") {
		t.Fatal("tick does not return the cursor")
	}
	if strings.Contains(patch, "\x1b7") || strings.Contains(patch, "\x1b8") {
		t.Fatal(patch)
	}
	if !strings.Contains(patch, "✧") {
		t.Fatal(patch)
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

func TestWordmarkSitsLeftOfBella(t *testing.T) {
	first := stripANSI(wordmark(0))
	later := stripANSI(wordmark(2))
	if first == later {
		t.Fatalf("mark did not change: %q", first)
	}
	if !strings.HasPrefix(first, "· Bella") {
		t.Fatal(first)
	}
	if !strings.HasPrefix(later, "✧ Bella") {
		t.Fatal(later)
	}
	plain := stripANSI(render(screen{width: 40, height: 16, mode: "auto"}))
	if !strings.Contains(plain, "· Bella") {
		t.Fatal(plain)
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

func TestRenderSessionShowsTheAgent(t *testing.T) {
	run := &liveRun{
		agent: "claude",
		done:  true,
		lines: []transcriptLine{
			{kind: "tool", text: "Bash  go test"},
			{kind: "text", text: "all passed"},
		},
	}
	raw := render(screen{
		width: 60, height: 24, mode: "auto", live: run,
		agents: []Agent{{
			Name: "claude", Bin: "/bin/claude",
			Usage: Usage{
				FiveHour: Window{Used: 4, Known: true},
				Weekly:   Window{Used: 46, Known: true},
			},
		}},
	})
	plain := stripANSI(raw)
	for _, want := range []string{"claude", "Bash  go test", "all passed", "> ", "esc back"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "5h") || strings.Contains(plain, "week") {
		t.Fatalf("agent usage is still on the conversation\n%s", plain)
	}
	if strings.Contains(plain, "Reply") || strings.Contains(plain, "│") {
		t.Fatalf("composer chrome leaked\n%s", plain)
	}
	rows := frameRows(raw)
	prompt := 0
	for i, row := range rows {
		if row == "> " {
			prompt = i
			break
		}
	}
	rule := strings.Repeat("─", 60)
	if prompt == 0 || !strings.Contains(rows[prompt-1], "claude") || rows[prompt+1] != rule {
		t.Fatalf("prompt rules\n%q", rows)
	}
	if strings.Index(plain, "claude") < strings.Index(plain, "Bash  go test") {
		t.Fatal("agent row is still above the conversation")
	}
	if !strings.Contains(raw, "\x1b[?25h") {
		t.Fatal("reply caret is hidden")
	}
}

func TestInputEditStaysOnThePromptLine(t *testing.T) {
	next, patch, col, ok := inputEditPatch([]rune("ab"), key{r: 'c'}, 5, 40)
	if !ok || string(next) != "abc" || patch != "c" || col != 6 {
		t.Fatalf("%q %q %d %v", string(next), patch, col, ok)
	}
	next, patch, col, ok = inputEditPatch([]rune("안"), key{r: 127}, 5, 40)
	if !ok || len(next) != 0 || patch != "\x1b[2D\x1b[K" || col != 3 {
		t.Fatalf("%q %q %d %v", string(next), patch, col, ok)
	}
	if _, _, _, ok = inputEditPatch([]rune("ab"), key{r: '\r'}, 5, 40); ok {
		t.Fatal("enter repaints")
	}
}

func TestTranscriptShowsWhenItWasWritten(t *testing.T) {
	at := time.Date(2026, 10, 19, 15, 4, 0, 0, time.Local)
	run := &liveRun{
		agent: "claude",
		done:  true,
		lines: []transcriptLine{
			{kind: "user", text: "fix the test", at: at},
			{kind: "text", text: "all passed", at: at},
		},
	}
	rows := frameRows(render(screen{width: 60, height: 24, live: run}))
	ask := lineWith(rows, "fix the test")
	answer := lineWith(rows, "all passed")
	if !strings.HasPrefix(ask, "fix the test") || !strings.HasSuffix(ask, "15:04") {
		t.Fatalf("request time %q", ask)
	}
	if !strings.HasPrefix(answer, "▎ all passed") || !strings.HasSuffix(answer, "15:04") {
		t.Fatalf("answer time %q", answer)
	}
	if strWidth(ask) != 60 || strWidth(answer) != 60 {
		t.Fatalf("widths %d %d", strWidth(ask), strWidth(answer))
	}
}

func TestLoadingSpinnerFollowsTheRequest(t *testing.T) {
	run := &liveRun{
		agent: "claude",
		lines: []transcriptLine{{kind: "user", text: "fix the test"}},
	}
	rows := frameRows(render(screen{width: 60, height: 24, frame: 1, live: run}))
	plain := strings.Join(rows, "\n")
	if !strings.Contains(plain, "fix the test\n\n▎ ⠙") {
		t.Fatalf("loader\n%s", plain)
	}
	run.done = true
	plain = strings.Join(frameRows(render(screen{width: 60, height: 24, frame: 1, live: run})), "\n")
	if strings.Contains(plain, "⠙") || strings.Contains(plain, "⠋") {
		t.Fatal(plain)
	}
	patch := framePatch(1, 20, 3, 8)
	if !strings.Contains(patch, "\x1b[8;1H\x1b[2K") || !strings.Contains(patch, "⠙") || !strings.Contains(patch, "\x1b[20;3H") {
		t.Fatal(patch)
	}
	if strings.Contains(patch, "\x1b7") || strings.Contains(patch, "\x1b8") {
		t.Fatal(patch)
	}
}

func TestTranscriptSeparatesRequestAndAnswer(t *testing.T) {
	run := &liveRun{
		agent: "claude",
		done:  true,
		lines: []transcriptLine{
			{kind: "user", text: "fix the test"},
			{kind: "text", text: "all passed"},
			{kind: "user", text: "and this"},
			{kind: "text", text: "done"},
		},
	}
	rows := frameRows(render(screen{width: 60, height: 24, live: run}))
	plain := strings.Join(rows, "\n")
	if strings.Contains(plain, "you") {
		t.Fatalf("speaker label leaked\n%s", plain)
	}
	if lineWith(rows, "fix the test") != "fix the test" || lineWith(rows, "and this") != "and this" {
		t.Fatalf("request is not left aligned\n%q", rows)
	}
	ask := strings.Index(plain, "fix the test")
	answer := strings.Index(plain, "▎ all passed")
	next := strings.Index(plain, "and this")
	done := strings.Index(plain, "▎ done")
	if ask < 0 || answer < ask || next < answer || done < next {
		t.Fatalf("%q", rows)
	}
}

func lineWith(rows []string, part string) string {
	for _, row := range rows {
		if strings.Contains(row, part) {
			return row
		}
	}
	return ""
}

func frameRows(raw string) []string {
	parts := strings.Split(raw, "\x1b[2K")
	rows := make([]string, 0, len(parts))
	for _, part := range parts[1:] {
		end := len(part)
		for i := 0; i < len(part); i++ {
			if part[i] != 0x1b || i+1 >= len(part) || part[i+1] != '[' {
				continue
			}
			j := i + 2
			for j < len(part) && (part[j] < 0x40 || part[j] > 0x7e) {
				j++
			}
			if j < len(part) && part[j] != 'm' {
				end = i
				break
			}
			i = j
		}
		rows = append(rows, stripANSI(part[:end]))
	}
	return rows
}

func TestTalkPinsModelAndEffort(t *testing.T) {
	run := &liveRun{
		agent:  "claude",
		model:  "claude-opus-5-5",
		effort: "xhigh",
		done:   true,
		lines:  []transcriptLine{{kind: "text", text: "all passed"}},
	}
	rows := frameRows(render(screen{width: 60, height: 24, live: run}))
	plain := strings.Join(rows, "\n")
	answerAt := strings.Index(plain, "all passed")
	title := ""
	for i, row := range rows {
		if row == "> " && i > 0 {
			title = rows[i-1]
			break
		}
	}
	if !strings.Contains(title, "claude") || !strings.Contains(title, "Opus 5.5") || !strings.Contains(title, "xhigh") || strWidth(title) != 60 {
		t.Fatalf("composer title %q\n%q", title, rows)
	}
	if strings.Index(plain, "Opus 5.5") < answerAt {
		t.Fatal("model is still pinned above the conversation")
	}
	if prettyModel("gpt-5.6-sol") != "GPT 5.6 Sol" || prettyModel("grok-4.7") != "Grok 4.7" {
		t.Fatal(prettyModel("gpt-5.6-sol"), prettyModel("grok-4.7"))
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
	for _, want := range []string{"Bella", "auto", "claude", "week 46%", "grok", "off", "> 리뷰", "^S settings"} {
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
