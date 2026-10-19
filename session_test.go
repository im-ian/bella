package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func feedAll(lines ...string) []transcriptLine {
	run := &liveRun{agent: "claude"}
	for _, line := range lines {
		run.accept(line)
	}
	return run.snapshot()
}

func kinds(lines []transcriptLine) string {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString(line.kind)
		b.WriteString(":")
		b.WriteString(line.text)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestClaudeStreamShowsTextAndTool(t *testing.T) {
	run := &liveRun{agent: "claude", model: "opus", effort: "xhigh"}
	for _, line := range []string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-4-6","effortLevel":"high"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Look"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ing"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looking"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"result","subtype":"success","result":"Looking"}`,
	} {
		run.accept(line)
	}
	got := kinds(run.snapshot())
	for _, want := range []string{"text:Looking", "tool:Bash  go test", "meta:ok"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "claude-sonnet") || strings.Contains(got, "high") {
		t.Fatalf("status leaked into the transcript\n%s", got)
	}
	if strings.Count(got, "text:Looking") != 1 {
		t.Fatalf("duplicated text\n%s", got)
	}
	model, effort := run.status()
	if model != "claude-sonnet-4-6" || effort != "high" {
		t.Fatalf("status %s %s", model, effort)
	}
}

func TestCodexStreamShowsCommandAndAnswer(t *testing.T) {
	got := kinds(feedAll(
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"item.started","item":{"id":"1","type":"command_execution","command":"ls","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"1","type":"command_execution","command":"ls","aggregated_output":"README.md\n","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"2","type":"agent_message","text":"Found the readme."}}`,
	))
	for _, want := range []string{"tool:ls", "meta:README.md", "text:Found the readme."} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n%s", want, got)
		}
	}
	if strings.Count(got, "tool:ls") != 1 {
		t.Fatalf("duplicated command\n%s", got)
	}
}

func TestGrokStreamShowsTextAndTool(t *testing.T) {
	run := &liveRun{}
	for _, line := range []string{
		`{"type":"system","subtype":"init","session_id":"ses_g","model":"grok-4.7"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hi"},{"type":"tool_use","name":"read_file","input":{"path":"main.go"}}]}}`,
	} {
		run.accept(line)
	}
	got := kinds(run.snapshot())
	for _, want := range []string{"text:Hi", "tool:read_file  main.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n%s", want, got)
		}
	}
	if run.sessionID() != "ses_g" {
		t.Fatal(run.sessionID())
	}
}

func TestStartRunShowsStreamedText(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\necho '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello from claude\"}]}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := startRun(Agent{Name: "claude", Bin: bin}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	defer run.stop()
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = kinds(run.snapshot())
		if strings.Contains(got, "text:hello from claude") && run.finished() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(got)
}

func TestStartRunKeepsTheTail(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
awk 'BEGIN {
  s = ""
  for (i = 0; i < 20000; i++) s = s "xxxxxxxxxx"
  printf "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"%s\"}]}}\n", s
  print "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"ses_tail\"}"
  print "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"TAILMARK\"}]}}"
}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := startRun(Agent{Name: "claude", Bin: bin}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	defer run.stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if run.finished() && run.sessionID() == "ses_tail" && strings.Contains(kinds(run.snapshot()), "text:TAILMARK") {
			run.stop()
			if run.pid != 0 {
				t.Fatal(run.pid)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("id=%s\n%s", run.sessionID(), kinds(run.snapshot()))
}

func TestAgentStdinIsNotATerminal(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
if [ -t 0 ]; then
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"TTY"}]}}'
else
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"QUIET"}]}}'
fi
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	run, err := startRun(Agent{Name: "claude", Bin: bin}, "say hi")
	if err != nil {
		t.Fatal(err)
	}
	defer run.stop()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := kinds(run.snapshot())
		if strings.Contains(got, "text:QUIET") && run.finished() {
			return
		}
		if strings.Contains(got, "text:TTY") {
			t.Fatal(got)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(kinds(run.snapshot()))
}

func TestResumeArgs(t *testing.T) {
	claude := resumeArgs(Agent{Name: "claude", Args: []string{"--dangerously-skip-permissions"}}, "ses_1", "and this")
	if strings.Join(claude, " ") != "-p --resume ses_1 --output-format stream-json --verbose --dangerously-skip-permissions and this" {
		t.Fatal(claude)
	}
	codex := resumeArgs(Agent{Name: "codex", Args: []string{"--yolo"}}, "thr_1", "and this")
	if strings.Join(codex, " ") != "--yolo exec resume --json thr_1 and this" {
		t.Fatal(codex)
	}
	grok := resumeArgs(Agent{Name: "grok"}, "ses_1", "and this")
	if strings.Join(grok, " ") != "--output-format streaming-messages-json --resume ses_1 -p and this" {
		t.Fatal(grok)
	}
}

func TestConfiguredStatus(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"opus","effortLevel":"xhigh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	model, effort := configuredStatus("claude")
	if model != "opus" || effort != "xhigh" {
		t.Fatalf("%s %s", model, effort)
	}
	codex := t.TempDir()
	if err := os.WriteFile(filepath.Join(codex, "config.toml"), []byte("model = \"gpt-5.6-sol\"\nmodel_reasoning_effort = \"max\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codex)
	model, effort = configuredStatus("codex")
	if model != "gpt-5.6-sol" || effort != "max" {
		t.Fatalf("%s %s", model, effort)
	}
}

func TestRememberSessionID(t *testing.T) {
	codex := &liveRun{}
	codex.accept(`{"type":"thread.started","thread_id":"thr_1"}`)
	if codex.sessionID() != "thr_1" {
		t.Fatal(codex.sessionID())
	}
	claude := &liveRun{}
	claude.accept(`{"type":"system","subtype":"init","session_id":"ses_1","model":"sonnet"}`)
	if claude.sessionID() != "ses_1" {
		t.Fatal(claude.sessionID())
	}
}

func TestSessionArgs(t *testing.T) {
	claude := sessionArgs(Agent{Name: "claude", Args: []string{"--dangerously-skip-permissions"}}, "fix it")
	if strings.Join(claude, " ") != "-p --output-format stream-json --verbose --dangerously-skip-permissions fix it" {
		t.Fatal(claude)
	}
	codex := sessionArgs(Agent{Name: "codex", Args: []string{"--yolo"}}, "fix it")
	if strings.Join(codex, " ") != "--yolo exec --json fix it" {
		t.Fatal(codex)
	}
	grok := sessionArgs(Agent{Name: "grok"}, "fix it")
	if strings.Join(grok, " ") != "--output-format streaming-messages-json -p fix it" {
		t.Fatal(grok)
	}
}
