package tui

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ANSI Color and Style Constants
const (
	Reset     = "\033[0m"
	Bold      = "\033[1m"
	Dim       = "\033[2m"
	Italic    = "\033[3m"
	Underline = "\033[4m"

	Red     = "\033[31m"
	Green   = "\033[32m"
	Yellow  = "\033[33m"
	Blue    = "\033[34m"
	Magenta = "\033[35m"
	Cyan    = "\033[36m"
	White   = "\033[37m"

	BgRed    = "\033[41m"
	BgGreen  = "\033[42m"
	BgYellow = "\033[43m"
	BgBlue   = "\033[44m"
)

// Theme defines colors for UI components.
type Theme struct {
	Primary   string
	Secondary string
	Success   string
	Warning   string
	Error     string
	Info      string
	Border    string
}

// DefaultTheme returns the default color scheme.
func DefaultTheme() Theme {
	return Theme{
		Primary:   Cyan,
		Secondary: Magenta,
		Success:   Green,
		Warning:   Yellow,
		Error:     Red,
		Info:      Blue,
		Border:    Dim,
	}
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// StripANSI removes ANSI escape codes from string.
func StripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

// VisibleWidth returns the visual character width of string without ANSI escape codes.
func VisibleWidth(s string) int {
	return len([]rune(StripANSI(s)))
}

// PadRight pads a string with spaces to visual width.
func PadRight(s string, width int) string {
	vw := VisibleWidth(s)
	if vw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vw)
}

// Status represents component or system status.
type Status struct {
	Code    int
	Message string
	Level   string
}

// RenderBanner renders a stylized title banner.
func RenderBanner(title string, theme Theme) string {
	bar := strings.Repeat("=", VisibleWidth(title)+4)
	return fmt.Sprintf("%s%s\n| %s |\n%s%s", theme.Primary, bar, title, bar, Reset)
}

// RenderStatusBar renders a status line.
func RenderStatusBar(status Status, theme Theme) string {
	color := theme.Info
	if status.Level == "ERROR" || status.Code >= 400 {
		color = theme.Error
	} else if status.Level == "WARN" {
		color = theme.Warning
	} else if status.Level == "OK" || status.Code == 200 {
		color = theme.Success
	}
	return fmt.Sprintf("%s[%s %d] %s%s", color, status.Level, status.Code, status.Message, Reset)
}

// RenderProgressBar renders a progress bar.
func RenderProgressBar(current, total, width int, theme Theme) string {
	if total <= 0 {
		return fmt.Sprintf("%s[%s]%s", theme.Border, strings.Repeat("-", width), Reset)
	}
	ratio := float64(current) / float64(total)
	if ratio > 1.0 {
		ratio = 1.0
	}
	filled := int(ratio * float64(width))
	empty := width - filled
	return fmt.Sprintf("%s[%s%s%s]%s %d%%", theme.Border, theme.Primary+strings.Repeat("#", filled), theme.Border, strings.Repeat("-", empty), Reset, int(ratio*100))
}

// RenderBox renders content inside a bordered box with a title.
func RenderBox(title, content string, theme Theme) string {
	lines := strings.Split(content, "\n")
	maxLen := VisibleWidth(title)
	for _, l := range lines {
		if w := VisibleWidth(l); w > maxLen {
			maxLen = w
		}
	}

	top := fmt.Sprintf("%s+-- %s %s+", theme.Border, title, strings.Repeat("-", maxLen-VisibleWidth(title)))
	bottom := fmt.Sprintf("%s+%s+", theme.Border, strings.Repeat("-", maxLen+4))

	var res []string
	res = append(res, top)
	for _, l := range lines {
		padded := PadRight(l, maxLen)
		res = append(res, fmt.Sprintf("%s|  %s  |", theme.Border, padded))
	}
	res = append(res, bottom+Reset)
	return strings.Join(res, "\n")
}

// RenderToolCard renders a card for a tool execution.
func RenderToolCard(name, desc string, status Status, theme Theme) string {
	header := fmt.Sprintf("%sTool: %s%s", theme.Primary, name, Reset)
	body := fmt.Sprintf("%s\nStatus: %s", desc, RenderStatusBar(status, theme))
	return RenderBox(header, body, theme)
}

// FormatDiff formats text diff output.
func FormatDiff(oldText, newText string) string {
	var sb strings.Builder
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")

	for _, l := range oldLines {
		if l != "" {
			sb.WriteString(fmt.Sprintf("%s- %s%s\n", Red, l, Reset))
		}
	}
	for _, l := range newLines {
		if l != "" {
			sb.WriteString(fmt.Sprintf("%s+ %s%s\n", Green, l, Reset))
		}
	}
	return sb.String()
}

// TodoItem represents a item in todo list.
type TodoItem struct {
	ID    int
	Title string
	Done  bool
}

// RenderTodoBoard renders a list of todo items.
func RenderTodoBoard(items []TodoItem, theme Theme) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s=== TODO BOARD ===%s\n", theme.Primary, Reset))
	for _, item := range items {
		check := "[ ]"
		color := theme.Warning
		if item.Done {
			check = "[X]"
			color = theme.Success
		}
		sb.WriteString(fmt.Sprintf("%s%s %d. %s%s\n", color, check, item.ID, item.Title, Reset))
	}
	return sb.String()
}

// RenderMarkdown performs basic markdown rendering for terminal display.
func RenderMarkdown(md string) string {
	lines := strings.Split(md, "\n")
	var res []string
	for _, l := range lines {
		if strings.HasPrefix(l, "# ") {
			res = append(res, fmt.Sprintf("%s%s%s", Bold+Cyan, strings.TrimPrefix(l, "# "), Reset))
		} else if strings.HasPrefix(l, "## ") {
			res = append(res, fmt.Sprintf("%s%s%s", Bold+Magenta, strings.TrimPrefix(l, "## "), Reset))
		} else if strings.HasPrefix(l, "- ") {
			res = append(res, fmt.Sprintf("  %s•%s %s", Yellow, Reset, strings.TrimPrefix(l, "- ")))
		} else {
			res = append(res, l)
		}
	}
	return strings.Join(res, "\n")
}

// PromptString formats a user prompt.
func PromptString(prompt string) string {
	return fmt.Sprintf("%s%s > %s", Bold+Cyan, prompt, Reset)
}

// Spinner represents a terminal spinner.
type Spinner struct {
	mu     sync.Mutex
	label  string
	active bool
	stopCh chan struct{}
}

// NewSpinner constructs a new Spinner instance.
func NewSpinner(label string) *Spinner {
	return &Spinner{
		label: label,
	}
}

// Start starts the spinner animation.
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	s.active = true
	s.stopCh = make(chan struct{})
	s.mu.Unlock()

	go func() {
		frames := []string{"|", "/", "-", "\\"}
		idx := 0
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.mu.Lock()
				lbl := s.label
				act := s.active
				s.mu.Unlock()
				if !act {
					return
				}
				_ = idx
				_ = lbl
				idx = (idx + 1) % len(frames)
			}
		}
	}()
}

// UpdateLabel updates spinner text safely.
func (s *Spinner) UpdateLabel(label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.label = label
}

// Stop stops the spinner animation safely.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	close(s.stopCh)
	s.mu.Unlock()
}
