package main

import (
	"fmt"
	"io"
	"strings"
)

func formatDecision(d Decision) string {
	if !d.Found {
		return d.Reason
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s · %s\n", d.Pick, classLabel(d.Class), policyLabel(d.Policy))
	fmt.Fprintf(&b, "%s\n", d.Reason)
	parts := make([]string, 0, len(d.Scores))
	for _, score := range d.Scores {
		if score.Known {
			parts = append(parts, fmt.Sprintf("%s %.0f (%.0f%%)", score.Name, score.Value, score.Pressure))
		} else {
			parts = append(parts, fmt.Sprintf("%s %.0f (no usage)", score.Name, score.Value))
		}
	}
	fmt.Fprintf(&b, "score  %s", strings.Join(parts, "  "))
	return b.String()
}

func writeStatus(w io.Writer, agents []Agent) {
	fmt.Fprintln(w, "bella")
	for _, agent := range agents {
		mark := "missing"
		if agent.Bin != "" {
			mark = agent.Bin
		}
		if agent.Disabled {
			mark = "off"
		}
		fmt.Fprintf(w, "  %-6s  %s  %s\n", agent.Name, formatWindows(agent.Usage), mark)
		if agent.Bin != "" && agent.Usage.Error != "" && !agent.Usage.FiveHour.Known && !agent.Usage.Weekly.Known {
			fmt.Fprintf(w, "          %s\n", agent.Usage.Error)
		}
	}
	fmt.Fprintln(w, "  grok weekly usage is 100 minus remaining credits")
}

func formatWindows(u Usage) string {
	five := "  -"
	week := "  -"
	if u.FiveHour.Known {
		five = fmt.Sprintf("%3.0f%%", u.FiveHour.Used)
	}
	if u.Weekly.Known {
		week = fmt.Sprintf("%3.0f%%", u.Weekly.Used)
	}
	if !u.FiveHour.Known && !u.Weekly.Known {
		return "5h    -   week   -  "
	}
	return fmt.Sprintf("5h %s   week %s", five, week)
}
