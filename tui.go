package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type screen struct {
	width    int
	height   int
	agents   []Agent
	input    []rune
	live     *liveRun
	scroll   int
	settings bool
	cursor   int
	mode     string
	notice   string
	frame    int
}

type key struct {
	r       rune
	special string
}

func runTUI() error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		writeStatus(os.Stdout, discover(loadConfig()))
		fmt.Fprintln(os.Stdout, "\nto hand off work: bella \"task\"")
		return nil
	}

	cfg := loadConfig()
	agents := discover(cfg)
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	fd := int(os.Stdin.Fd())
	defer term.Restore(fd, state)
	// Alternate screen, wrap off. A line as wide as the terminal would
	// otherwise wrap and shove the next row down.
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?7l\x1b[2J\x1b[H")
	defer fmt.Fprint(os.Stdout, "\x1b[?1049l\x1b[?7h\x1b[?25h")

	var input []rune
	var live *liveRun
	settings := false
	cursor := 0
	scroll := 0
	notice := ""
	shown := -1
	caretRow, caretCol := 0, 0
	loaderRow := 0
	defer func() {
		if live != nil {
			live.stop()
		}
	}()
	for {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || width < 8 {
			width = 80
		}
		if err != nil || height < 4 {
			height = 24
		}
		frame := int(time.Now().UnixMilli() / 200)
		text, row, col, load := paint(screen{
			width: width, height: height, agents: agents, input: input,
			live: live, scroll: scroll, settings: settings, cursor: cursor, mode: cfg.Mode, notice: notice,
			frame: frame,
		})
		caretRow, caretCol, loaderRow = row, col, load
		if _, err := os.Stdout.WriteString(text); err != nil {
			return err
		}
		shown = frame
		var k key
		var kind string
		for {
			k, kind, err = nextEvent(fd, live, !settings)
			if err != nil {
				return err
			}
			if kind != "tick" {
				// Typing stays on the prompt line. A full frame moves the
				// cursor through every row, and the caret follows it.
				if kind == "key" && !settings && caretRow > 0 {
					if next, patch, col, ok := inputEditPatch(input, k, caretCol, width); ok {
						input = next
						caretCol = col
						if patch != "" {
							if _, err := os.Stdout.WriteString(patch); err != nil {
								return err
							}
						}
						continue
					}
				}
				break
			}
			// The spark only changes. A full repaint would hide and show the
			// cursor and restart its blink on every tick.
			frame = int(time.Now().UnixMilli() / 200)
			if frame == shown || settings {
				continue
			}
			shown = frame
			if _, err := os.Stdout.WriteString(framePatch(frame, caretRow, caretCol, loaderRow)); err != nil {
				return err
			}
		}
		if kind == "output" {
			continue
		}
		if k.r == 3 || k.r == 4 {
			if live != nil {
				live.stop()
			}
			return nil
		}
		if live != nil {
			switch {
			case k.special == "esc":
				live.stop()
				live = nil
				input = nil
				scroll = 0
				notice = ""
			case k.special == "up":
				scroll++
			case k.special == "down":
				if scroll > 0 {
					scroll--
				}
			case k.r == '\r' || k.r == '\n':
				if !live.finished() {
					continue
				}
				text := strings.TrimSpace(string(input))
				if text == "" {
					continue
				}
				if err := live.reply(text); err != nil {
					notice = err.Error()
					continue
				}
				input = nil
				scroll = 0
				notice = ""
			case k.r == 127, k.r == 8:
				if len(input) > 0 {
					input = input[:len(input)-1]
				}
			default:
				if k.r < 32 || k.special != "" {
					continue
				}
				if len(input) < 4000 {
					input = append(input, k.r)
				}
			}
			continue
		}
		if settings {
			rows := settingRows()
			switch {
			case k.special == "esc":
				settings = false
				notice = ""
			case k.special == "up" || k.r == 'k':
				cursor--
				if cursor < 0 {
					cursor = len(rows) - 1
				}
			case k.special == "down" || k.r == 'j':
				cursor++
				if cursor >= len(rows) {
					cursor = 0
				}
			case k.r == ' ' || k.r == '\r' || k.r == '\n':
				row := rows[cursor]
				if row.kind == "agent" {
					on := !cfg.enabled(row.id)
					if err := setAgent(&cfg, agents, row.id, on); err != nil {
						notice = "could not save"
					} else {
						notice = ""
					}
				} else if cfg.Mode != row.id {
					if err := setMode(&cfg, row.id); err != nil {
						notice = "could not save"
					} else {
						notice = ""
					}
				}
			}
			continue
		}
		if k.special == "esc" {
			input = nil
			continue
		}
		if k.r == '\t' || k.special == "backtab" {
			dir := 1
			if k.special == "backtab" {
				dir = -1
			}
			if err := setMode(&cfg, cycleMode(cfg.Mode, dir)); err != nil {
				notice = "could not save"
			} else {
				notice = ""
			}
			continue
		}
		if k.r == 19 { // ctrl-s
			settings = true
			cursor = 0
			notice = ""
			continue
		}
		switch k.r {
		case '\r', '\n':
			task := strings.TrimSpace(string(input))
			if task == "" {
				continue
			}
			decision := decide(task, agents, cfg)
			if !decision.Found {
				notice = decision.Reason
				continue
			}
			run, err := startRun(decision.Agent, task)
			if err != nil {
				notice = err.Error()
				continue
			}
			live = run
			input = nil
			scroll = 0
			notice = ""
		case 127, 8:
			if len(input) > 0 {
				input = input[:len(input)-1]
			}
		default:
			if k.r < 32 || k.special != "" {
				continue
			}
			if len(input) < 4000 {
				input = append(input, k.r)
			}
		}
	}
}

type settingRow struct {
	kind string
	id   string
}

func settingRows() []settingRow {
	rows := make([]settingRow, 0, len(agentNames)+3)
	for _, name := range agentNames {
		rows = append(rows, settingRow{kind: "agent", id: name})
	}
	for _, mode := range []string{"auto", policyBalance, policyBest} {
		rows = append(rows, settingRow{kind: "mode", id: mode})
	}
	return rows
}

func routingModes() []string {
	return []string{"auto", policyBalance, policyBest}
}

func cycleMode(current string, dir int) string {
	modes := routingModes()
	idx := 0
	for i, mode := range modes {
		if mode == current {
			idx = i
			break
		}
	}
	idx = (idx + dir) % len(modes)
	if idx < 0 {
		idx += len(modes)
	}
	return modes[idx]
}

func setMode(cfg *Config, mode string) error {
	cfg.Mode = mode
	return cfg.save()
}

func setAgent(cfg *Config, agents []Agent, name string, on bool) error {
	if cfg.Enabled == nil {
		cfg.Enabled = map[string]bool{}
	}
	cfg.Enabled[name] = on
	for i := range agents {
		if agents[i].Name == name {
			agents[i].Disabled = !on
		}
	}
	return cfg.save()
}

func render(s screen) string {
	text, _, _, _ := paint(s)
	return text
}

func paint(s screen) (string, int, int, int) {
	var lines []string
	row, col, loader := 0, 0, 0
	if s.settings {
		lines = renderSettings(s)
	} else if s.live != nil {
		lines, row, col, loader = renderTalk(s)
	} else {
		lines, row, col = renderHome(s)
	}
	if s.height > 0 && len(lines) > s.height {
		drop := len(lines) - s.height
		lines = lines[drop:]
		if row > 0 {
			row -= drop
			if row < 1 {
				row, col = 0, 0
			}
		}
		if loader > 0 {
			loader -= drop
			if loader < 1 {
				loader = 0
			}
		}
	}
	var b strings.Builder
	// Synchronized update keeps the cursor from being shown at each row
	// while the frame is painted. A plain home move is visible on every key.
	b.WriteString("\x1b[?2026h\x1b[?1049h\x1b[?7l\x1b[?25l")
	for i, line := range lines {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", i+1, fitLine(line, s.width))
	}
	b.WriteString("\x1b[J")
	if row > 0 && col > 0 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", row, col)
	}
	b.WriteString("\x1b[?2026l")
	return b.String(), row, col, loader
}

func wordmarkPatch(frame, row, col int) string {
	return framePatch(frame, row, col, 0)
}

func framePatch(frame, row, col, loaderRow int) string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[?25l\x1b[1;1H")
	b.WriteString(wordmark(frame))
	if loaderRow > 0 {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", loaderRow, loaderLine(frame))
	}
	if row > 0 && col > 0 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", row, col)
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

func loaderLine(frame int) string {
	spins := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	colors := []string{"240", "245", "252", "255", "252", "245"}
	spin := spins[frame%len(spins)]
	color := colors[frame%len(colors)]
	return answerPrefix() + "\x1b[38;5;" + color + "m" + spin + "\x1b[0m"
}

func fitLine(s string, width int) string {
	if width < 1 || visibleWidth(s) <= width {
		return s
	}
	return clipWidth(stripANSI(s), width)
}

func renderHome(s screen) ([]string, int, int) {
	top := []string{
		wordmark(s.frame),
		dim(strings.Repeat("─", s.width)),
		modeChips(s.mode),
		"",
	}
	if len(s.agents) == 0 {
		top = append(top, dim("no agents"))
	}
	for _, agent := range s.agents {
		top = append(top, agentHomeLine(agent))
	}
	footer := dim("tab mode    enter run    ^S settings    ^C quit")
	content, caretCol := composerLine(string(s.input), s.width)
	bottom := promptBlock(content, "", s.width)
	if s.notice != "" {
		bottom = append(bottom, dim(s.notice))
	}
	bottom = append(bottom, "", footer)
	lines, promptAt := pin(top, bottom, s.height)
	// promptAt is the rule above the prompt.
	return lines, promptAt + 2, caretCol
}

func renderTalk(s screen) ([]string, int, int, int) {
	agent := talkAgent(s)
	model, effort := s.live.status()
	header := []string{
		wordmark(s.frame),
		dim(strings.Repeat("─", s.width)),
		"",
	}
	content, caretCol := composerLine(string(s.input), s.width)
	bottom := promptBlock(content, composerTitle(agent.Name, model, effort, s.width), s.width)
	if s.notice != "" {
		bottom = append(bottom, dim(s.notice))
	}
	footer := dim("enter reply    esc back    ↑↓ scroll    ^C quit")
	if !s.live.finished() {
		footer = dim("running    esc back    ↑↓ scroll    ^C quit")
	}
	bottom = append(bottom, "", footer)
	room := s.height - len(header) - len(bottom)
	if room < 0 {
		room = 0
	}
	transcript, loaderAt := transcriptBody(s.live.snapshot(), s.width, room, s.scroll, s.frame, !s.live.finished())
	for len(transcript) < room {
		transcript = append(transcript, "")
	}
	lines := append(append([]string{}, header...), transcript...)
	promptAt := len(lines)
	lines = append(lines, bottom...)
	loaderRow := 0
	if loaderAt >= 0 {
		loaderRow = len(header) + loaderAt + 1
	}
	return lines, promptAt + 2, caretCol, loaderRow
}

func blankLine(lines *[]string) {
	if len(*lines) > 0 && (*lines)[len(*lines)-1] != "" {
		*lines = append(*lines, "")
	}
}

func answerPrefix() string {
	return "\x1b[38;5;240m▎\x1b[0m "
}

func talkAgent(s screen) Agent {
	for _, agent := range s.agents {
		if agent.Name == s.live.agent {
			return agent
		}
	}
	agent := s.live.src
	agent.Name = s.live.agent
	if agent.Bin == "" {
		agent.Bin = s.live.agent
	}
	return agent
}

func transcriptBody(lines []transcriptLine, width, room, scroll, frame int, loading bool) ([]string, int) {
	if width < 1 {
		width = 1
	}
	if room < 1 {
		return nil, -1
	}
	var wrapped []string
	afterUser := false
	for _, line := range lines {
		text := cleanTerm(line.text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if line.kind == "user" {
			if len(wrapped) > 0 {
				blankLine(&wrapped)
			}
			wrapped = append(wrapped, stampRows(text, width, width, line.at, func(row string) string {
				return "\x1b[1m" + row + "\x1b[0m"
			})...)
			afterUser = true
			continue
		}
		if afterUser {
			blankLine(&wrapped)
			afterUser = false
		}
		if line.kind == "tool" {
			text = "▸ " + text
		}
		bodyWidth := width - 2
		if bodyWidth < 1 {
			bodyWidth = 1
		}
		when := time.Time{}
		if line.kind == "text" || line.kind == "error" {
			when = line.at
		}
		wrapped = append(wrapped, stampRows(text, width, bodyWidth, when, func(row string) string {
			switch line.kind {
			case "meta", "tool":
				row = dim(row)
			case "error":
				row = "\x1b[31m" + row + "\x1b[0m"
			}
			return answerPrefix() + row
		})...)
	}
	loaderAt := -1
	if loading && scroll == 0 {
		if afterUser {
			blankLine(&wrapped)
		}
		if room > 0 && (len(wrapped) < room || afterUser) {
			if len(wrapped) >= room {
				wrapped = wrapped[:room-1]
			}
			loaderAt = len(wrapped)
			wrapped = append(wrapped, loaderLine(frame))
		}
	}
	if len(wrapped) == 0 {
		return []string{dim("running")}, -1
	}
	if len(wrapped) <= room {
		return wrapped, loaderAt
	}
	end := len(wrapped) - scroll
	if end < room {
		end = room
	}
	if end > len(wrapped) {
		end = len(wrapped)
	}
	start := end - room
	if start < 0 {
		start = 0
	}
	if loaderAt >= 0 {
		if loaderAt < start || loaderAt >= end {
			loaderAt = -1
		} else {
			loaderAt -= start
		}
	}
	return wrapped[start:end], loaderAt
}

func stampRows(text string, screenWidth, contentWidth int, at time.Time, paint func(string) string) []string {
	when := ""
	if !at.IsZero() {
		when = at.Format("15:04")
	}
	var rows []string
	first := true
	for _, part := range strings.Split(text, "\n") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		remain := part
		for remain != "" {
			room := contentWidth
			if first && when != "" {
				room -= strWidth(when) + 1
				if room < 1 {
					room = 1
				}
			}
			var row string
			row, remain = takeWidth(remain, room)
			painted := paint(row)
			if first && when != "" {
				painted = withRightMeta(painted, when, screenWidth)
			}
			rows = append(rows, painted)
			first = false
		}
	}
	return rows
}

func withRightMeta(left, meta string, width int) string {
	metaW := strWidth(meta)
	if metaW == 0 || metaW >= width {
		return left
	}
	padN := width - visibleWidth(left) - metaW
	if padN < 1 {
		padN = 1
	}
	return left + strings.Repeat(" ", padN) + dim(meta)
}

func takeWidth(s string, width int) (string, string) {
	if s == "" || width < 1 {
		return "", s
	}
	w := 0
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if w+rw > width && i > 0 {
			break
		}
		w += rw
		i += size
		if w >= width {
			break
		}
	}
	return s[:i], s[i:]
}

func wordmark(frame int) string {
	marks := []struct {
		glyph string
		color string
	}{
		{"·", "240"},
		{"✦", "245"},
		{"✧", "252"},
		{"✶", "255"},
		{"✧", "252"},
		{"✦", "245"},
	}
	mark := marks[frame%len(marks)]
	return "\x1b[38;5;" + mark.color + "m" + mark.glyph + "\x1b[0m \x1b[1mBella\x1b[0m"
}

func nextEvent(stdin int, live *liveRun, animate bool) (key, string, error) {
	var notify *os.File
	if live != nil {
		notify = live.notifyR
	}
	for {
		var set unix.FdSet
		set.Set(stdin)
		maxfd := stdin
		nfd := -1
		if notify != nil {
			nfd = int(notify.Fd())
			set.Set(nfd)
			if nfd > maxfd {
				maxfd = nfd
			}
		}
		var timeout *unix.Timeval
		if animate {
			timeout = &unix.Timeval{Usec: 200_000}
		}
		n, err := unix.Select(maxfd+1, &set, nil, nil, timeout)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return key{}, "", err
		}
		if n == 0 {
			return key{}, "tick", nil
		}
		if nfd >= 0 && set.IsSet(nfd) {
			buf := make([]byte, 256)
			got, readErr := notify.Read(buf)
			if got == 0 || readErr == io.EOF {
				if live.notifyR == notify {
					live.notifyR = nil
				}
				notify.Close()
			}
			return key{}, "output", nil
		}
		k, err := readKey(stdin)
		return k, "key", err
	}
}

func renderSettings(s screen) []string {
	top := []string{
		headerLine("Bella", "settings", s.width),
		dim(strings.Repeat("─", s.width)),
		"",
		dim("agents"),
	}
	rows := settingRows()
	if s.cursor < 0 {
		s.cursor = 0
	}
	if s.cursor >= len(rows) {
		s.cursor = len(rows) - 1
	}
	byName := map[string]Agent{}
	for _, agent := range s.agents {
		byName[agent.Name] = agent
	}
	rowIndex := 0
	for _, row := range rows {
		if row.kind == "mode" && rowIndex == len(agentNames) {
			top = append(top, "", dim("routing"))
		}
		var plain string
		if row.kind == "agent" {
			plain = agentMenuLabel(byName[row.id])
		} else {
			plain = modeMenuLabel(row.id, s.mode)
		}
		top = append(top, menuLine(plain, s.width, rowIndex == s.cursor))
		rowIndex++
	}
	footer := dim("↑↓ move    space toggle    esc close")
	bottom := []string{}
	if s.notice != "" {
		bottom = append(bottom, dim(s.notice))
	}
	bottom = append(bottom, "", footer)
	lines, _ := pin(top, bottom, s.height)
	return lines
}

func composerTitle(agent, model, effort string, width int) string {
	var parts []string
	if name := strings.TrimSpace(agent); name != "" {
		parts = append(parts, name)
	}
	if pretty := prettyModel(model); pretty != "" {
		parts = append(parts, pretty)
	}
	if text := strings.TrimSpace(effort); text != "" {
		parts = append(parts, text)
	}
	if len(parts) == 0 {
		return ""
	}
	return labeledRule(strings.Join(parts, "  "), width)
}

func labeledRule(label string, width int) string {
	if width < 1 {
		width = 1
	}
	label = strings.TrimSpace(label)
	if label == "" || width < 4 {
		return dim(strings.Repeat("─", width))
	}
	room := width - 3
	if room < 1 {
		room = 1
	}
	if strWidth(label) > room {
		label = clipWidth(label, room)
	}
	dashes := width - 2 - strWidth(label) - 1
	if dashes < 0 {
		dashes = 0
	}
	return dim("─ ") + "\x1b[1m" + label + "\x1b[0m" + dim(" "+strings.Repeat("─", dashes))
}

func prettyModel(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "["); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	raw = strings.TrimPrefix(raw, "claude-")
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return ""
	}
	var out []string
	for i := 0; i < len(parts); i++ {
		if i+1 < len(parts) && digitsOnly(parts[i]) && digitsOnly(parts[i+1]) && len(parts[i+1]) == 1 {
			out = append(out, parts[i]+"."+parts[i+1])
			i++
			continue
		}
		out = append(out, titleToken(parts[i]))
	}
	return strings.Join(out, " ")
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func titleToken(s string) string {
	if strings.EqualFold(s, "gpt") {
		return "GPT"
	}
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func agentHomeLine(agent Agent) string {
	mark := "●"
	if agent.Disabled || agent.Bin == "" {
		mark = "○"
	}
	text := mark + "  " + pad(agent.Name, 8) + pad(agentStatus(agent), 22)
	if agent.Disabled || agent.Bin == "" {
		return dim(text)
	}
	return text + usageBar(agent.Usage, 16)
}

func agentStatus(agent Agent) string {
	if agent.Bin == "" {
		return "not installed"
	}
	if agent.Disabled {
		return shortUsage(agent.Usage) + "   off"
	}
	return shortUsage(agent.Usage)
}

func agentMenuLabel(agent Agent) string {
	mark := "[x]"
	if agent.Disabled {
		mark = "[ ]"
	}
	state := "not installed"
	if agent.Bin != "" {
		state = shortUsage(agent.Usage)
	}
	return mark + "  " + pad(agent.Name, 8) + state
}

func modeMenuLabel(mode, current string) string {
	if current == "" {
		current = "auto"
	}
	mark := "( )"
	if mode == current {
		mark = "(*)"
	}
	return mark + "  " + modeLabel(mode)
}

func modeChips(mode string) string {
	if mode == "" {
		mode = "auto"
	}
	var b strings.Builder
	for i, id := range routingModes() {
		if i > 0 {
			b.WriteString("  ")
		}
		label := " " + modeLabel(id) + " "
		if id == mode {
			b.WriteString("\x1b[7m" + label + "\x1b[0m")
		} else {
			b.WriteString(dim(label))
		}
	}
	return b.String()
}

func promptBlock(content, title string, width int) []string {
	if width < 1 {
		width = 1
	}
	top := dim(strings.Repeat("─", width))
	if title != "" {
		top = title
	}
	return []string{top, content, dim(strings.Repeat("─", width))}
}

func composerLine(input string, width int) (string, int) {
	const prefix = "> "
	room := width - strWidth(prefix)
	if room < 1 {
		room = 1
	}
	shown := ""
	if input != "" {
		shown = tailWidth(input, room)
	}
	return prefix + shown, strWidth(prefix) + strWidth(shown) + 1
}

// inputEditPatch reports a same-line update for a typed character or a
// backspace. Anything else, and input already clipped to the row, needs a
// full paint.
func inputEditPatch(input []rune, k key, col, width int) (next []rune, patch string, newCol int, ok bool) {
	if k.special != "" || col < 1 || width < 3 {
		return input, "", col, false
	}
	room := width - 2
	if strWidth(string(input)) > room {
		return input, "", col, false
	}
	if k.r == 127 || k.r == 8 {
		if len(input) == 0 {
			return input, "", col, true
		}
		last := input[len(input)-1]
		w := runeWidth(last)
		if w < 1 || col-w < 1 {
			return input, "", col, false
		}
		return input[:len(input)-1], fmt.Sprintf("\x1b[%dD\x1b[K", w), col - w, true
	}
	if k.r < 32 || len(input) >= 4000 {
		return input, "", col, false
	}
	w := runeWidth(k.r)
	if strWidth(string(input))+w > room || col+w > width {
		return input, "", col, false
	}
	next = make([]rune, len(input)+1)
	copy(next, input)
	next[len(input)] = k.r
	return next, string(k.r), col + w, true
}

func headerLine(left, right string, width int) string {
	gap := width - strWidth(left) - strWidth(right)
	if gap < 1 {
		gap = 1
	}
	return "\x1b[1m" + left + "\x1b[0m" + strings.Repeat(" ", gap) + "\x1b[2m" + right + "\x1b[0m"
}

func menuLine(plain string, width int, selected bool) string {
	plain = clipWidth(plain, width)
	padded := pad(plain, width)
	if selected {
		return "\x1b[7m" + padded + "\x1b[0m"
	}
	return padded
}

func pin(top, bottom []string, height int) ([]string, int) {
	if height < 1 {
		height = 1
	}
	if len(bottom) > height-1 {
		bottom = bottom[:height-1]
	}
	room := height - len(bottom)
	if len(top) > room {
		top = top[:room]
	}
	gap := room - len(top)
	lines := append([]string{}, top...)
	for i := 0; i < gap; i++ {
		lines = append(lines, "")
	}
	bottomAt := len(lines)
	return append(lines, bottom...), bottomAt
}

func shortUsage(u Usage) string {
	parts := make([]string, 0, 2)
	if u.FiveHour.Known {
		parts = append(parts, fmt.Sprintf("5h %.0f%%", u.FiveHour.Used))
	}
	if u.Weekly.Known {
		parts = append(parts, fmt.Sprintf("week %.0f%%", u.Weekly.Used))
	}
	if len(parts) == 0 {
		if u.Error != "" {
			return u.Error
		}
		return "no usage"
	}
	return strings.Join(parts, "  ")
}

func usageBar(u Usage, width int) string {
	used, known := pressure(u)
	if !known {
		return dim(strings.Repeat("░", width))
	}
	filled := filledCount(used, width)
	color := ""
	if used >= 90 {
		color = "\x1b[31m"
	}
	return color + strings.Repeat("█", filled) + "\x1b[0m" + dim(strings.Repeat("░", width-filled))
}

func filledCount(used float64, width int) int {
	filled := int(used/100*float64(width) + 0.5)
	if filled > width {
		return width
	}
	if filled < 0 {
		return 0
	}
	return filled
}

func dim(s string) string {
	return "\x1b[2m" + s + "\x1b[0m"
}

func pad(s string, width int) string {
	w := strWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func tailWidth(s string, width int) string {
	if width < 1 || s == "" {
		return ""
	}
	if strWidth(s) <= width {
		return s
	}
	runes := []rune(s)
	w := 0
	i := len(runes)
	for i > 0 {
		rw := runeWidth(runes[i-1])
		if w+rw > width-1 {
			break
		}
		w += rw
		i--
	}
	return "…" + string(runes[i:])
}

func clipWidth(s string, width int) string {
	if width < 1 {
		return ""
	}
	if strWidth(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > width-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	b.WriteRune('…')
	return b.String()
}

func wrapWidth(s string, width int) []string {
	if strWidth(s) <= width {
		return []string{s}
	}
	var lines []string
	var cur []rune
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > width && len(cur) > 0 {
			lines = append(lines, string(cur))
			cur = cur[:0]
			w = 0
		}
		cur = append(cur, r)
		w += rw
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func strWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

func visibleWidth(s string) int {
	return strWidth(stripANSI(s))
}

func cleanTerm(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == 0x1b {
			i += skipEscape(s[i:])
			continue
		}
		if r == '\n' {
			b.WriteByte('\n')
			i += size
			continue
		}
		if r < 0x20 || r == 0x7f {
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func skipEscape(s string) int {
	if len(s) < 2 || s[0] != 0x1b {
		return 1
	}
	switch s[1] {
	case '[':
		i := 2
		for i < len(s) {
			c := s[i]
			i++
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		return i
	case ']':
		i := 2
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return len(s)
	default:
		return 2
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += skipEscape(s[i:])
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func runeWidth(r rune) int {
	if r < 32 || r == 0x7f || unicode.Is(unicode.Mn, r) {
		return 0
	}
	if isWide(r) {
		return 2
	}
	return 1
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE10 && r <= 0xFE19,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1FAFF:
		return true
	default:
		return false
	}
}

func readKey(fd int) (key, error) {
	r, err := readRune(fd)
	if err != nil {
		return key{}, err
	}
	if r != 0x1b {
		return key{r: r}, nil
	}
	tail := readEscapeTail(fd)
	switch {
	case tail == "":
		return key{special: "esc"}, nil
	case strings.HasSuffix(tail, "A"):
		return key{special: "up"}, nil
	case strings.HasSuffix(tail, "B"):
		return key{special: "down"}, nil
	case strings.HasSuffix(tail, "C"):
		return key{special: "right"}, nil
	case strings.HasSuffix(tail, "D"):
		return key{special: "left"}, nil
	case strings.HasSuffix(tail, "Z"):
		return key{special: "backtab"}, nil
	default:
		return key{special: "esc"}, nil
	}
}

func readRune(fd int) (rune, error) {
	var buf [4]byte
	if err := readFull(fd, buf[:1]); err != nil {
		return 0, err
	}
	n := utf8Len(buf[0])
	if n > 1 {
		if err := readFull(fd, buf[1:n]); err != nil {
			return 0, err
		}
	}
	rn, _ := utf8.DecodeRune(buf[:n])
	return rn, nil
}

func readFull(fd int, buf []byte) error {
	for len(buf) > 0 {
		n, err := readFD(fd, buf)
		if n > 0 {
			buf = buf[n:]
		}
		if len(buf) == 0 {
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.EOF
		}
	}
	return nil
}

func readFD(fd int, buf []byte) (int, error) {
	for {
		n, err := unix.Read(fd, buf)
		if err == unix.EINTR {
			continue
		}
		if n == 0 && err == nil {
			return 0, io.EOF
		}
		return n, err
	}
}

func utf8Len(b byte) int {
	switch {
	case b&0x80 == 0:
		return 1
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	case b&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

func readEscapeTail(fd int) string {
	var set unix.FdSet
	set.Set(fd)
	tv := unix.Timeval{Usec: 20_000}
	n, err := unix.Select(fd+1, &set, nil, nil, &tv)
	if err != nil || n == 0 {
		return ""
	}
	var buf [8]byte
	nread, err := readFD(fd, buf[:])
	if err != nil || nread == 0 {
		return ""
	}
	return string(buf[:nread])
}
