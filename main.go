package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	args := os.Args[1:]
	dry := false
	if len(args) > 0 && (args[0] == "-n" || args[0] == "--dry-run") {
		dry = true
		args = args[1:]
	}
	if len(args) == 0 {
		if err := runTUI(); err != nil {
			fmt.Fprintln(os.Stderr, "bella:", err)
			os.Exit(1)
		}
		return
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(helpText)
		return
	case "status":
		writeStatus(os.Stdout, discover(loadConfig()))
		return
	case "init":
		path, err := saveDefaultConfig()
		if err != nil {
			fmt.Fprintln(os.Stderr, "bella:", err)
			os.Exit(1)
		}
		fmt.Println(path)
		return
	case "plan":
		dry = true
		args = args[1:]
	}

	task := strings.TrimSpace(strings.Join(args, " "))
	if task == "" {
		fmt.Fprintln(os.Stderr, "bella: write the task to hand off")
		os.Exit(2)
	}
	cfg := loadConfig()
	decision := decide(task, discover(cfg), cfg)
	fmt.Fprintln(os.Stdout, formatDecision(decision))
	if dry || !decision.Found {
		if !decision.Found {
			os.Exit(1)
		}
		return
	}
	if err := handoff(decision.Agent, task); err != nil {
		fmt.Fprintln(os.Stderr, "bella:", err)
		os.Exit(1)
	}
}

const helpText = `bella hands work to whichever of claude, codex, or grok is installed.

  bella                     show usage and take a task. enter opens the conversation, a reply continues it, tab changes mode, ^S opens settings
  bella "fix the flaky test"  choose an agent and hand the terminal to it
  bella plan "review this"    show the choice without handing off
  bella status
  bella init                write ~/.bella/config.json if it does not exist

mode is auto, balance, or best. args holds extra flags for each CLI.
bella does not use shell aliases, so a permission-skip flag on claude is not passed unless it is listed in args.
`
