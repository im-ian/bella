package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type transcriptLine struct {
	kind string
	text string
	at   time.Time
}

// liveRun is one agent CLI streaming into the bella screen.
type liveRun struct {
	agent   string
	src     Agent
	id      string
	model   string
	effort  string
	mu      sync.Mutex
	lines   []transcriptLine
	draft   string
	draftAt time.Time
	items   map[string]bool
	done    bool
	stopped bool
	errText string
	cmd     *exec.Cmd
	notifyR *os.File
	notifyW *os.File
}

func sessionArgs(agent Agent, prompt string) []string {
	prompt = strings.TrimSpace(prompt)
	switch agent.Name {
	case "claude":
		args := []string{"-p", "--output-format", "stream-json", "--verbose"}
		args = append(args, agent.Args...)
		return append(args, promptArg(prompt)...)
	case "codex":
		// Root flags such as --yolo belong before the exec subcommand.
		args := append([]string{}, agent.Args...)
		args = append(args, "exec", "--json")
		return append(args, promptArg(prompt)...)
	case "grok":
		args := append([]string{}, agent.Args...)
		args = append(args, "--output-format", "streaming-messages-json", "-p", prompt)
		return args
	default:
		args := append([]string{}, agent.Args...)
		return append(args, promptArg(prompt)...)
	}
}

func resumeArgs(agent Agent, id, prompt string) []string {
	prompt = strings.TrimSpace(prompt)
	switch agent.Name {
	case "claude":
		args := []string{"-p", "--resume", id, "--output-format", "stream-json", "--verbose"}
		args = append(args, agent.Args...)
		return append(args, promptArg(prompt)...)
	case "codex":
		args := append([]string{}, agent.Args...)
		args = append(args, "exec", "resume", "--json", id)
		return append(args, promptArg(prompt)...)
	case "grok":
		args := append([]string{}, agent.Args...)
		args = append(args, "--output-format", "streaming-messages-json", "--resume", id, "-p", prompt)
		return args
	default:
		return sessionArgs(agent, prompt)
	}
}

func promptArg(prompt string) []string {
	if strings.HasPrefix(prompt, "-") {
		return []string{"--", prompt}
	}
	return []string{prompt}
}

func startRun(agent Agent, prompt string) (*liveRun, error) {
	if agent.Bin == "" {
		return nil, fmt.Errorf("%s is not on PATH", agent.Name)
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("write the task to hand off")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(agent.Bin, sessionArgs(agent, prompt)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	// Setsid starts a new session, so the agent has no controlling terminal
	// and cannot redraw over bella. Its pid is the process group id.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return nil, err
	}
	model, effort := configuredStatus(agent.Name)
	run := &liveRun{
		agent:   agent.Name,
		src:     agent,
		model:   model,
		effort:  effort,
		lines:   []transcriptLine{{kind: "user", text: prompt, at: time.Now()}},
		items:   map[string]bool{},
		cmd:     cmd,
		notifyR: pr,
		notifyW: pw,
	}
	go run.scan(stdout)
	go run.scan(stderr)
	go func() {
		err := cmd.Wait()
		run.finish(err)
	}()
	return run, nil
}

func (r *liveRun) scan(rd io.Reader) {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for sc.Scan() {
		r.accept(sc.Text())
	}
}

func (r *liveRun) finish(err error) {
	r.mu.Lock()
	r.done = true
	if err != nil && !r.stopped {
		r.errText = err.Error()
	}
	r.commitDraft()
	w := r.notifyW
	r.notifyW = nil
	r.mu.Unlock()
	if w != nil {
		_, _ = w.Write([]byte{1})
		w.Close()
	}
}

func (r *liveRun) stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.stopped = true
	pid := 0
	if r.cmd != nil && r.cmd.Process != nil {
		pid = r.cmd.Process.Pid
	}
	r.mu.Unlock()
	if pid <= 0 {
		return
	}
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
}

func (r *liveRun) poke() {
	r.mu.Lock()
	w := r.notifyW
	r.mu.Unlock()
	if w == nil {
		return
	}
	_, _ = w.Write([]byte{1})
}

func (r *liveRun) reply(prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("write the task to hand off")
	}
	r.mu.Lock()
	id := r.id
	agent := r.src
	done := r.done
	r.mu.Unlock()
	if !done {
		return fmt.Errorf("still running")
	}
	if id == "" {
		return fmt.Errorf("could not resume this conversation")
	}
	if agent.Bin == "" {
		return fmt.Errorf("%s is not on PATH", agent.Name)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd := exec.Command(agent.Bin, resumeArgs(agent, id, prompt)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	// Setsid starts a new session, so the agent has no controlling terminal
	// and cannot redraw over bella. Its pid is the process group id.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	r.mu.Lock()
	oldR := r.notifyR
	r.done = false
	r.stopped = false
	r.errText = ""
	r.draft = ""
	r.items = map[string]bool{}
	r.cmd = cmd
	r.notifyR = pr
	r.notifyW = pw
	r.add(transcriptLine{kind: "user", text: prompt})
	r.mu.Unlock()
	if oldR != nil {
		oldR.Close()
	}
	go r.scan(stdout)
	go r.scan(stderr)
	go func() {
		err := cmd.Wait()
		r.finish(err)
	}()
	return nil
}

func (r *liveRun) sessionID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.id
}

func (r *liveRun) finished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

func (r *liveRun) snapshot() []transcriptLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := append([]transcriptLine(nil), r.lines...)
	if strings.TrimSpace(r.draft) != "" {
		lines = append(lines, transcriptLine{kind: "text", text: r.draft, at: r.draftAt})
	}
	if r.done && r.errText != "" {
		lines = append(lines, transcriptLine{kind: "error", text: r.errText})
	}
	return lines
}

func (r *liveRun) accept(raw string) {
	r.mu.Lock()
	r.acceptLocked(strings.TrimSpace(raw))
	r.mu.Unlock()
	r.poke()
}

func (r *liveRun) acceptLocked(raw string) {
	if raw == "" {
		return
	}
	if r.items == nil {
		r.items = map[string]bool{}
	}
	if !strings.HasPrefix(raw, "{") {
		r.add(transcriptLine{kind: "meta", text: clipBlock(stripANSI(raw))})
		return
	}
	r.takeID(raw)
	r.takeStatus(raw)
	var head struct {
		Type   string `json:"type"`
		Method string `json:"method"`
	}
	if json.Unmarshal([]byte(raw), &head) != nil {
		r.add(transcriptLine{kind: "meta", text: clipBlock(raw)})
		return
	}
	switch {
	case head.Type == "assistant" || head.Type == "user" || head.Type == "system" || head.Type == "result" || head.Type == "stream_event" || strings.HasPrefix(head.Type, "content_block") || head.Type == "message_delta":
		r.acceptClaude(raw)
	case strings.HasPrefix(head.Type, "item.") || head.Type == "turn.failed" || head.Type == "error":
		r.acceptCodex(raw)
	case head.Method == "session/update" || head.Type == "session/update":
		r.acceptACP(raw)
	}
}

func (r *liveRun) takeID(raw string) {
	if r.id != "" {
		return
	}
	var probe struct {
		SessionID string `json:"session_id"`
		ThreadID  string `json:"thread_id"`
		SessionId string `json:"sessionId"`
		Params    struct {
			SessionID  string `json:"sessionId"`
			SessionID2 string `json:"session_id"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil {
		return
	}
	switch {
	case probe.SessionID != "":
		r.id = probe.SessionID
	case probe.ThreadID != "":
		r.id = probe.ThreadID
	case probe.SessionId != "":
		r.id = probe.SessionId
	case probe.Params.SessionID != "":
		r.id = probe.Params.SessionID
	case probe.Params.SessionID2 != "":
		r.id = probe.Params.SessionID2
	}
}

func (r *liveRun) status() (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.model, r.effort
}

func (r *liveRun) takeStatus(raw string) {
	var probe struct {
		Model     string `json:"model"`
		Effort    string `json:"effortLevel"`
		EffortAlt string `json:"effort"`
		Reasoning string `json:"model_reasoning_effort"`
		Message   struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil {
		return
	}
	model := probe.Model
	if model == "" {
		model = probe.Message.Model
	}
	effort := probe.Effort
	if effort == "" {
		effort = probe.EffortAlt
	}
	if effort == "" {
		effort = probe.Reasoning
	}
	r.noteLocked(model, effort, true)
}

func (r *liveRun) noteLocked(model, effort string, fromStream bool) {
	if model != "" && (fromStream || r.model == "") {
		r.model = model
	}
	if effort != "" && (fromStream || r.effort == "") {
		r.effort = effort
	}
}

func configuredStatus(agent string) (string, string) {
	switch agent {
	case "claude":
		return readClaudeStatus(filepath.Join(homePath("CLAUDE_CONFIG_DIR", ".claude"), "settings.json"))
	case "codex":
		return readTOMLPair(filepath.Join(homePath("CODEX_HOME", ".codex"), "config.toml"), "model", "model_reasoning_effort")
	case "grok":
		return readTOMLPair(filepath.Join(homePath("GROK_HOME", ".grok"), "config.toml"), "default", "default_reasoning_effort")
	default:
		return "", ""
	}
}

func readClaudeStatus(path string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var cfg struct {
		Model  string `json:"model"`
		Effort string `json:"effortLevel"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return "", ""
	}
	return strings.TrimSpace(cfg.Model), strings.TrimSpace(cfg.Effort)
}

func readTOMLPair(path, modelKey, effortKey string) (string, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	return tomlString(string(data), modelKey), tomlString(string(data), effortKey)
}

func tomlString(data, key string) string {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		return strings.Trim(v, `"'`)
	}
	return ""
}

func (r *liveRun) acceptClaude(raw string) {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Model   string `json:"model"`
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
		} `json:"event"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
		ContentBlock struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content_block"`
	}
	if json.Unmarshal([]byte(raw), &ev) != nil {
		return
	}
	switch ev.Type {
	case "system":
		return
	case "result":
		if ev.IsError || strings.Contains(ev.Subtype, "error") {
			if text := strings.TrimSpace(ev.Result); text != "" {
				r.add(transcriptLine{kind: "error", text: clipBlock(text)})
			}
		}
	case "stream_event":
		r.acceptWire(ev.Event.Type, ev.Event.Delta.Type, ev.Event.Delta.Text, ev.Event.ContentBlock.Type, ev.Event.ContentBlock.Name)
	case "content_block_delta", "content_block_start":
		r.acceptWire(ev.Type, ev.Delta.Type, ev.Delta.Text, ev.ContentBlock.Type, ev.ContentBlock.Name)
	case "assistant", "user":
		r.acceptContent(ev.Type, ev.Message.Content)
	}
}

func (r *liveRun) acceptWire(eventType, deltaType, deltaText, blockType, blockName string) {
	switch eventType {
	case "content_block_delta":
		if deltaType == "text_delta" && deltaText != "" {
			r.noteDraft(deltaText)
		}
	case "content_block_start":
		if blockType == "tool_use" && blockName != "" {
			r.commitDraft()
			r.addTool(blockName)
		}
	}
}

func (r *liveRun) acceptContent(role string, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil && role == "assistant" {
			r.takeText(text)
		}
		return
	}
	var blocks []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Name    string          `json:"name"`
		Input   map[string]any  `json:"input"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if role == "assistant" {
				r.takeText(block.Text)
			}
		case "tool_use":
			r.commitDraft()
			r.addTool(toolSummary(block.Name, block.Input))
		case "tool_result":
			if text := contentText(block.Content); text != "" {
				r.add(transcriptLine{kind: "meta", text: clipBlock(text)})
			}
		}
	}
}

func (r *liveRun) acceptCodex(raw string) {
	var ev struct {
		Type    string          `json:"type"`
		Item    codexItem       `json:"item"`
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal([]byte(raw), &ev) != nil {
		return
	}
	if ev.Type == "error" || ev.Type == "turn.failed" {
		text := strings.TrimSpace(ev.Message)
		if text == "" {
			text = rawMessage(ev.Error)
		}
		if text != "" {
			r.add(transcriptLine{kind: "error", text: clipBlock(text)})
		}
		return
	}
	if ev.Type != "item.started" && ev.Type != "item.completed" {
		return
	}
	item := ev.Item
	kind := item.Type
	if kind == "" {
		kind = item.ItemType
	}
	id := item.ID
	switch kind {
	case "agent_message", "assistant_message":
		if ev.Type == "item.completed" {
			r.takeText(item.Text)
		}
	case "reasoning":
		if ev.Type == "item.completed" && strings.TrimSpace(item.Text) != "" {
			r.add(transcriptLine{kind: "meta", text: clipBlock(item.Text)})
		}
	case "command_execution":
		if id == "" || !r.items[id] {
			if cmd := oneLine(item.Command); cmd != "" {
				r.addTool(cmd)
			}
			if id != "" {
				r.items[id] = true
			}
		}
		if ev.Type == "item.completed" {
			outID := id + "#out"
			if strings.TrimSpace(item.AggregatedOutput) != "" && (id == "" || !r.items[outID]) {
				r.add(transcriptLine{kind: "meta", text: clipBlock(item.AggregatedOutput)})
				if id != "" {
					r.items[outID] = true
				}
			}
		}
	case "file_change":
		if ev.Type != "item.completed" {
			return
		}
		label := "edit"
		var paths []string
		for _, change := range item.Changes {
			if change.Path != "" {
				paths = append(paths, change.Path)
			}
		}
		if len(paths) > 0 {
			label = "edit  " + strings.Join(paths, ", ")
		}
		r.addTool(oneLine(label))
	case "web_search":
		if ev.Type == "item.completed" {
			r.addTool(toolSummary("search", map[string]any{"query": item.Query}))
		}
	case "mcp_tool_call":
		if ev.Type == "item.started" || id == "" || !r.items[id] {
			name := item.Name
			if name == "" {
				name = "mcp"
			}
			r.addTool(name)
			if id != "" {
				r.items[id] = true
			}
		}
	}
}

type codexItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	ItemType         string `json:"item_type"`
	Text             string `json:"text"`
	Command          string `json:"command"`
	AggregatedOutput string `json:"aggregated_output"`
	Query            string `json:"query"`
	Name             string `json:"name"`
	Changes          []struct {
		Path string `json:"path"`
	} `json:"changes"`
}

func (r *liveRun) acceptACP(raw string) {
	var ev struct {
		Params struct {
			Update struct {
				SessionUpdate string `json:"sessionUpdate"`
				Title         string `json:"title"`
				Kind          string `json:"kind"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(raw), &ev) != nil {
		return
	}
	update := ev.Params.Update
	switch update.SessionUpdate {
	case "agent_message_chunk":
		if update.Content.Text != "" {
			r.noteDraft(update.Content.Text)
		}
	case "tool_call":
		r.commitDraft()
		label := update.Title
		if label == "" {
			label = update.Kind
		}
		if label == "" {
			label = "tool"
		}
		r.addTool(oneLine(label))
	}
}

func (r *liveRun) takeText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if r.draft != "" && (text == strings.TrimSpace(r.draft) || strings.HasPrefix(text, r.draft)) {
		r.draft = ""
		r.addText(text)
		return
	}
	r.commitDraft()
	r.addText(text)
}

func (r *liveRun) commitDraft() {
	if strings.TrimSpace(r.draft) == "" {
		r.draft = ""
		return
	}
	text := r.draft
	r.draft = ""
	r.addText(text)
}

func (r *liveRun) noteDraft(text string) {
	if text == "" {
		return
	}
	if r.draft == "" {
		r.draftAt = time.Now()
	}
	r.draft += text
}

func (r *liveRun) addText(text string) {
	text = strings.TrimSpace(text)
	at := r.draftAt
	r.draftAt = time.Time{}
	if text == "" {
		return
	}
	if n := len(r.lines); n > 0 && r.lines[n-1].kind == "text" && r.lines[n-1].text == text {
		return
	}
	r.add(transcriptLine{kind: "text", text: text, at: at})
}

func (r *liveRun) addTool(text string) {
	text = oneLine(text)
	if text == "" {
		return
	}
	if n := len(r.lines); n > 0 && r.lines[n-1].kind == "tool" {
		prev := r.lines[n-1].text
		if prev == text || strings.HasPrefix(text, prev+" ") || strings.HasPrefix(text, prev+"  ") {
			r.lines[n-1].text = text
			return
		}
	}
	r.add(transcriptLine{kind: "tool", text: text})
}

func (r *liveRun) add(line transcriptLine) {
	if strings.TrimSpace(line.text) == "" {
		return
	}
	if line.at.IsZero() {
		line.at = time.Now()
	}
	r.lines = append(r.lines, line)
	if len(r.lines) > 500 {
		r.lines = append([]transcriptLine(nil), r.lines[len(r.lines)-500:]...)
		r.items = map[string]bool{}
	}
}

func toolSummary(name string, input map[string]any) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	for _, key := range []string{"command", "cmd", "file_path", "path", "pattern", "query", "url", "description"} {
		value, ok := input[key]
		if !ok || value == nil {
			continue
		}
		text := oneLine(fmt.Sprint(value))
		if text != "" {
			return name + "  " + text
		}
	}
	return name
}

func contentText(raw json.RawMessage) string {
	raw = bytesTrim(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, block := range blocks {
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func rawMessage(raw json.RawMessage) string {
	raw = bytesTrim(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return text
		}
		return ""
	}
	var obj struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.Message
	}
	return ""
}

func bytesTrim(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	runes := []rune(s)
	if len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return s
}

func clipBlock(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	parts := strings.Split(s, "\n")
	if len(parts) > 6 {
		parts = append(parts[:6], "…")
	}
	s = strings.Join(parts, "\n")
	runes := []rune(s)
	if len(runes) > 500 {
		return string(runes[:500]) + "…"
	}
	return s
}
