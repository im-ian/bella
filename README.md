# bella

Hands a task to one of the installed AI CLIs: `claude`, `codex`, or `grok`.

When usage is far apart, the task goes to whoever has room. When usage is close, it goes to the agent that fits the task. The choice is made from words in the task, with no model call.

```bash
bella
bella "fix the flaky test"
bella plan "review this change"
bella status
bella init
```

`bella "task"` prints the choice and then replaces the terminal with that CLI. With no arguments, bella shows usage and a task box. Enter confirms the handoff. `^S` toggles which agents are used. Those choices are saved to `~/.bella/config.json`.

Usage is read from files the CLIs already write.

- Claude: `~/.claude/token-widget/claude-rate-limits.json`
- Codex: `rate_limits` in recent `~/.codex/sessions` files
- Grok: weekly credits in `~/.grok/logs/unified.jsonl`. `creditUsagePercent` is remaining credit, so used percent is 100 minus that value.

Shell aliases are not applied. To pass `claude --dangerously-skip-permissions` or `codex --yolo`, put the flags in `args` after `bella init`.

```json
{
  "mode": "auto",
  "args": {
    "claude": ["--dangerously-skip-permissions"],
    "codex": ["--yolo"]
  }
}
```

`mode` is `auto`, `balance`, or `best`. `auto` picks the agent with room when usage differs by at least `balance_gap` percentage points (default 25).
