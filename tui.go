package main

import (
	"fmt"
	"io"
	"os"
	"strings"
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
	pending  *Decision
	settings bool
	cursor   int
	mode     string
	notice   string
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
	fmt.Fprint(os.Stdout, "\x1b[?1049h")
	defer fmt.Fprint(os.Stdout, "\x1b[?1049l\x1b[?25h")

	var input []rune
	var pending *Decision
	settings := false
	cursor := 0
	notice := ""
	for {
		width, height, err := term.GetSize(int(os.Stdout.Fd()))
		if err != nil || width < 20 {
			width = 80
		}
		if height < 12 {
			height = 24
		}
		if _, err := os.Stdout.WriteString(render(screen{
			width: width, height: height, agents: agents, input: input,
			pending: pending, settings: settings, cursor: cursor, mode: cfg.Mode, notice: notice,
		})); err != nil {
			return err
		}
		k, err := readKey(fd)
		if err != nil {
			return err
		}
		if k.r == 3 || k.r == 4 {
			return nil
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
			if pending != nil {
				pending = nil
				continue
			}
			input = nil
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
			if pending != nil {
				if !pending.Found {
					pending = nil
					continue
				}
				term.Restore(int(os.Stdin.Fd()), state)
				fmt.Fprint(os.Stdout, "\x1b[?1049l\x1b[?25h")
				fmt.Println(formatDecision(*pending))
				return handoff(pending.Agent, string(input))
			}
			task := strings.TrimSpace(string(input))
			if task == "" {
				continue
			}
			decision := decide(task, agents, cfg)
			pending = &decision
		case 127, 8:
			if pending != nil {
				continue
			}
			if len(input) > 0 {
				input = input[:len(input)-1]
			}
		default:
			if pending != nil || k.r < 32 || k.special != "" {
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
	var lines []string
	row, col := 0, 0
	if s.settings {
		lines = renderSettings(s)
	} else {
		lines, row, col = renderHome(s)
	}
	var b strings.Builder
	// Hide the cursor while painting so it does not sit on the footer,
	// then put the real cursor on the text that was just typed.
	b.WriteString("\x1b[?25l\x1b[H")
	b.WriteString(strings.Join(lines, "\r\n"))
	b.WriteString("\x1b[J")
	if row > 0 && col > 0 {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", row, col)
	}
	return b.String()
}

func renderHome(s screen) ([]string, int, int) {
	top := []string{
		headerLine("bella", modeLabel(s.mode), s.width),
		dim(strings.Repeat("─", s.width)),
		"",
	}
	if len(s.agents) == 0 {
		top = append(top, dim("no agents"))
	}
	for _, agent := range s.agents {
		top = append(top, agentHomeLine(agent))
	}
	var body []string
	footer := dim("enter choose    ^S settings    ^C quit")
	caretCol := 0
	if s.pending != nil {
		body = decisionBody(*s.pending, s.width-4)
		if s.pending.Found {
			footer = dim("enter hand off    esc cancel    ^C quit")
		} else {
			footer = dim("esc close    ^C quit")
		}
	} else {
		content, col := composerContent(string(s.input), s.width)
		body = []string{content}
		caretCol = col
	}
	title := "task"
	if s.pending != nil {
		title = "hand off"
	}
	bottom := roundedBox(title, body, s.width)
	if s.notice != "" {
		bottom = append(bottom, dim(s.notice))
	}
	bottom = append(bottom, "", footer)
	lines, bottomAt := pin(top, bottom, s.height)
	if caretCol == 0 {
		return lines, 0, 0
	}
	// bottomAt is the box top. The typed text is the next row.
	return lines, bottomAt + 2, caretCol
}

func renderSettings(s screen) []string {
	top := []string{
		headerLine("bella", "settings", s.width),
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

func decisionBody(d Decision, width int) []string {
	if width < 8 {
		width = 8
	}
	text := formatDecision(d)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		lines = append(lines, wrapWidth(line, width)...)
	}
	return lines
}

func composerContent(input string, width int) (string, int) {
	room := width - 4
	if room < 1 {
		room = 1
	}
	if input == "" {
		return dim(clipWidth("write a task", room)), 3
	}
	shown := tailWidth(input, room)
	return shown, 3 + strWidth(shown)
}

func roundedBox(title string, body []string, width int) []string {
	if width < 8 {
		width = 8
	}
	inner := width - 2
	label := " " + title + " "
	if strWidth(label) > inner {
		label = clipWidth(label, inner)
	}
	top := "╭" + label + strings.Repeat("─", inner-strWidth(label)) + "╮"
	if len(body) == 0 {
		body = []string{""}
	}
	lines := []string{top}
	for _, line := range body {
		lines = append(lines, boxMid(line, inner))
	}
	lines = append(lines, "╰"+strings.Repeat("─", inner)+"╯")
	return lines
}

func boxMid(content string, inner int) string {
	room := inner - 2
	if room < 1 {
		room = 1
	}
	padN := room - visibleWidth(content)
	if padN < 0 {
		content = clipWidth(stripANSI(content), room)
		padN = room - strWidth(content)
	}
	if padN < 0 {
		padN = 0
	}
	return "│ " + content + strings.Repeat(" ", padN) + " │"
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
	if height < len(bottom)+1 {
		height = len(bottom) + 1
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

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
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
