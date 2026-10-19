package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseClaude(t *testing.T) {
	five, week, ok := parseClaude([]byte(`{"rate_limits":{"five_hour":{"used_percentage":4,"resets_at":10},"seven_day":{"used_percentage":46,"resets_at":20}}}`))
	if !ok || five.Used != 4 || week.Used != 46 || week.ResetAt != 20 {
		t.Fatalf("%+v %+v ok=%v", five, week, ok)
	}
}

func TestParseCodexWeeklyPrimary(t *testing.T) {
	line := []byte(`{"payload":{"rate_limits":{"primary":{"used_percent":37,"window_minutes":10080,"resets_at":1787612904},"secondary":null}}}`)
	five, week, ok := parseCodexLine(line)
	if !ok || five.Known || week.Used != 37 {
		t.Fatalf("five=%+v week=%+v ok=%v", five, week, ok)
	}
}

func TestParseGrokRemainingCredits(t *testing.T) {
	line := []byte(`{"msg":"billing: fetched credits config","ctx":{"config":{"creditUsagePercent":3,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-10-26T00:33:32.018044+00:00"}}}}`)
	week, ok := parseGrokLine(line)
	if !ok || week.Used != 97 || !week.HasReset {
		t.Fatalf("%+v ok=%v", week, ok)
	}
}

func TestParseGrokSkipsNonWeekly(t *testing.T) {
	line := []byte(`{"ctx":{"config":{"creditUsagePercent":12,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","end":"2026-09-24T00:00:00Z"}}}}`)
	if _, ok := parseGrokLine(line); ok {
		t.Fatal("monthly period should be ignored")
	}
}

func TestReadCodexSkipsNewerSessionWithoutLimits(t *testing.T) {
	dir := t.TempDir()
	older := filepath.Join(dir, "old.jsonl")
	newer := filepath.Join(dir, "new.jsonl")
	if err := os.WriteFile(older, []byte("{\"payload\":{\"rate_limits\":{\"primary\":{\"used_percent\":11,\"window_minutes\":300}}}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, []byte("{\"payload\":{\"type\":\"noise\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldStamp := time.Now().Add(-time.Hour)
	newStamp := time.Now()
	if err := os.Chtimes(older, oldStamp, oldStamp); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, newStamp, newStamp); err != nil {
		t.Fatal(err)
	}
	usage := readCodex(dir)
	if !usage.FiveHour.Known || usage.FiveHour.Used != 11 {
		t.Fatalf("%+v", usage)
	}
}
