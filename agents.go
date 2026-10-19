package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

var agentNames = []string{"claude", "codex", "grok"}

func discover(cfg Config) []Agent {
	claudeUsage, codexUsage, grokUsage := readUsages()
	usages := map[string]Usage{
		"claude": claudeUsage,
		"codex":  codexUsage,
		"grok":   grokUsage,
	}
	agents := make([]Agent, 0, len(agentNames))
	for _, name := range agentNames {
		bin, _ := exec.LookPath(name)
		agents = append(agents, Agent{
			Name:     name,
			Bin:      bin,
			Usage:    usages[name],
			Fit:      cfg.Fit[name],
			Args:     cfg.Args[name],
			Disabled: !cfg.enabled(name),
		})
	}
	return agents
}

func handoff(agent Agent, prompt string) error {
	if agent.Bin == "" {
		return fmt.Errorf("%s is not on PATH", agent.Name)
	}
	argv := []string{agent.Bin}
	argv = append(argv, agent.Args...)
	prompt = strings.TrimSpace(prompt)
	if prompt != "" {
		if strings.HasPrefix(prompt, "-") {
			argv = append(argv, "--")
		}
		argv = append(argv, prompt)
	}
	return syscall.Exec(agent.Bin, argv, os.Environ())
}
