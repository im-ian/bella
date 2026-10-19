package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	codexScanFiles = 30
	codexTailBytes = 256 * 1024
	grokTailBytes  = 8 * 1024 * 1024
)

// Window is one rate-limit bucket. Used is 0 when the whole quota remains
// and 100 when it is spent.
type Window struct {
	Used     float64
	ResetAt  float64
	HasReset bool
	Known    bool
	Source   string
}

type Usage struct {
	FiveHour Window
	Weekly   Window
	Error    string
}

func homePath(envKey, fallback string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, fallback)
}

func readUsages() (claude, codex, grok Usage) {
	claude = readClaude(filepath.Join(homePath("CLAUDE_CONFIG_DIR", ".claude"), "token-widget", "claude-rate-limits.json"))
	codex = readCodex(filepath.Join(homePath("CODEX_HOME", ".codex"), "sessions"))
	grok = readGrok(filepath.Join(homePath("GROK_HOME", ".grok"), "logs", "unified.jsonl"))
	return claude, codex, grok
}

func readClaude(path string) Usage {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Usage{Error: "no usage file"}
		}
		return Usage{Error: err.Error()}
	}
	five, week, ok := parseClaude(data)
	if !ok {
		return Usage{Error: "could not read usage"}
	}
	five.Source = path
	week.Source = path
	return Usage{FiveHour: five, Weekly: week}
}

func parseClaude(data []byte) (five, week Window, ok bool) {
	var root struct {
		RateLimits struct {
			FiveHour *limitWindow `json:"five_hour"`
			SevenDay *limitWindow `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return Window{}, Window{}, false
	}
	five = windowFrom(root.RateLimits.FiveHour, "used_percentage")
	week = windowFrom(root.RateLimits.SevenDay, "used_percentage")
	return five, week, five.Known || week.Known
}

type limitWindow struct {
	UsedPercent    *float64 `json:"used_percent"`
	UsedPercentage *float64 `json:"used_percentage"`
	WindowMinutes  *float64 `json:"window_minutes"`
	ResetsAt       *float64 `json:"resets_at"`
	ResetAt        *float64 `json:"reset_at"`
}

func windowFrom(w *limitWindow, usedField string) Window {
	if w == nil {
		return Window{}
	}
	var used *float64
	if usedField == "used_percentage" {
		used = w.UsedPercentage
	} else {
		used = w.UsedPercent
	}
	if used == nil {
		return Window{}
	}
	out := Window{Used: clamp(*used), Known: true}
	if w.ResetsAt != nil {
		out.ResetAt = *w.ResetsAt
		out.HasReset = true
	} else if w.ResetAt != nil {
		out.ResetAt = *w.ResetAt
		out.HasReset = true
	}
	return out
}

func readCodex(sessions string) Usage {
	files, err := newestJSONL(sessions, codexScanFiles)
	if err != nil {
		if os.IsNotExist(err) {
			return Usage{Error: "no session directory"}
		}
		return Usage{Error: err.Error()}
	}
	for _, file := range files {
		tail, err := readTail(file, codexTailBytes)
		if err != nil {
			continue
		}
		for _, line := range reverseLines(tail) {
			if !bytes.Contains(line, []byte(`"rate_limits"`)) {
				continue
			}
			five, week, ok := parseCodexLine(line)
			if ok {
				five.Source = file
				week.Source = file
				return Usage{FiveHour: five, Weekly: week}
			}
		}
	}
	return Usage{Error: "no usage in recent sessions"}
}

func parseCodexLine(line []byte) (five, week Window, ok bool) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(line, &root); err != nil {
		return Window{}, Window{}, false
	}
	raw := root["rate_limits"]
	if payload, exists := root["payload"]; exists {
		var body map[string]json.RawMessage
		if json.Unmarshal(payload, &body) == nil {
			if inner, ok := body["rate_limits"]; ok {
				raw = inner
			}
		}
	}
	if len(raw) == 0 {
		return Window{}, Window{}, false
	}
	var limits struct {
		Primary   *limitWindow `json:"primary"`
		Secondary *limitWindow `json:"secondary"`
	}
	if err := json.Unmarshal(raw, &limits); err != nil {
		return Window{}, Window{}, false
	}
	put := func(w *limitWindow, fallback string) {
		parsed := windowFrom(w, "used_percent")
		if !parsed.Known || w == nil {
			return
		}
		kind := fallback
		if w.WindowMinutes != nil {
			switch int(*w.WindowMinutes) {
			case 300:
				kind = "five_hour"
			case 10080:
				kind = "weekly"
			}
		}
		if kind == "weekly" {
			week = parsed
		} else {
			five = parsed
		}
	}
	put(limits.Primary, "five_hour")
	put(limits.Secondary, "weekly")
	return five, week, five.Known || week.Known
}

func readGrok(path string) Usage {
	tail, err := readTail(path, grokTailBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return Usage{Error: "no usage log"}
		}
		return Usage{Error: err.Error()}
	}
	for _, line := range reverseLines(tail) {
		if !bytes.Contains(line, []byte("billing: fetched credits config")) {
			continue
		}
		week, ok := parseGrokLine(line)
		if ok {
			week.Source = path
			return Usage{Weekly: week}
		}
	}
	return Usage{Error: "no weekly usage in the recent log"}
}

func parseGrokLine(line []byte) (Window, bool) {
	var root struct {
		Ctx struct {
			Config struct {
				CreditUsagePercent *float64 `json:"creditUsagePercent"`
				BillingPeriodEnd   string   `json:"billingPeriodEnd"`
				CurrentPeriod      *struct {
					Type string `json:"type"`
					End  any    `json:"end"`
				} `json:"currentPeriod"`
			} `json:"config"`
		} `json:"ctx"`
	}
	if err := json.Unmarshal(line, &root); err != nil {
		return Window{}, false
	}
	cfg := root.Ctx.Config
	if cfg.CreditUsagePercent == nil {
		return Window{}, false
	}
	if cfg.CurrentPeriod != nil && cfg.CurrentPeriod.Type != "" && cfg.CurrentPeriod.Type != "USAGE_PERIOD_TYPE_WEEKLY" {
		return Window{}, false
	}
	// creditUsagePercent is remaining credits: 100 is full, 0 is spent.
	used := 100 - *cfg.CreditUsagePercent
	out := Window{Used: clamp(used), Known: true}
	end := ""
	if cfg.CurrentPeriod != nil {
		end = scalarString(cfg.CurrentPeriod.End)
	}
	if end == "" {
		end = cfg.BillingPeriodEnd
	}
	if reset, ok := parseReset(end); ok {
		out.ResetAt = float64(reset.Unix())
		out.HasReset = true
	}
	return out, true
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func parseReset(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func pressure(u Usage) (float64, bool) {
	best := 0.0
	known := false
	if u.FiveHour.Known && u.FiveHour.Used >= best {
		best = u.FiveHour.Used
		known = true
	}
	if u.Weekly.Known && (!known || u.Weekly.Used >= best) {
		best = u.Weekly.Used
		known = true
	}
	return best, known
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if info.Size() > max {
		start = info.Size() - max
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	return buf, nil
}

func reverseLines(buf []byte) [][]byte {
	parts := bytes.Split(buf, []byte("\n"))
	out := make([][]byte, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(parts[i])
		if len(line) == 0 {
			continue
		}
		out = append(out, line)
	}
	return out
}

func newestJSONL(root string, limit int) ([]string, error) {
	type item struct {
		path  string
		mtime time.Time
	}
	var items []item
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		items = append(items, item{path: path, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if _, statErr := os.Stat(root); statErr != nil {
			return nil, statErr
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].mtime.After(items[j].mtime)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	paths := make([]string, len(items))
	for i, item := range items {
		paths[i] = item.path
	}
	return paths, nil
}
