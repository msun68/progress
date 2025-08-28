package ui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

// SetPercentMsg updates the progress to a value in [0,1].
type SetPercentMsg struct{ Pct float64 }

// DoneMsg marks the line as complete (renders 100%).
type DoneMsg struct{}

// CancelMsg requests cancellation from the parent model (e.g., on Esc).
type CancelMsg struct{}

// ProgressLine is a reusable single-line progress component that renders:
//
//	Label...[====>] XX%
//
// It can be reused for any long-running task, not just docker pulls.
type ProgressLine struct {
	Label   string
	Percent float64
	Done    bool
	Width   int
}

// NewProgressLine creates a new progress line with a label.
func NewProgressLine(label string) *ProgressLine {
	pl := &ProgressLine{
		Label:   label,
		Percent: 0,
		Width:   40,
	}
	return pl
}

// InitCmd returns an optional command (no-op here).
func (p *ProgressLine) InitCmd() tea.Cmd { return nil }

// Update handles Bubble Tea messages for this component.
func (p *ProgressLine) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		// Esc/Ctrl+C => request cancel; otherwise swallow all keys by default
		switch m.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			return func() tea.Msg { return CancelMsg{} }, true
		default:
			return nil, true // swallow any other key
		}
	case SetPercentMsg:
		pct := m.Pct
		if pct < 0 {
			pct = 0
		}
		if pct > 1 {
			pct = 1
		}
		p.Percent = pct
		if pct >= 1 {
			p.Done = true
		}
		return nil, true
	case DoneMsg:
		p.Percent = 1
		p.Done = true
		return nil, true
	}
	return nil, false
}

func asciiBar(pct float64, width int) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}
	filled := int(pct * float64(width))
	if filled > width {
		filled = width
	}
	bar := make([]rune, width)
	for i := 0; i < width; i++ {
		bar[i] = ' '
	}
	if filled > 0 {
		for i := 0; i < filled-1; i++ {
			bar[i] = '='
		}
		bar[filled-1] = '>'
	}
	return string(bar)
}

// View returns the single-line string for this component.
func (p *ProgressLine) View() string {
	return fmt.Sprintf("%s...[%s] %3.0f%%", p.Label, asciiBar(p.Percent, p.Width), p.Percent*100)
}
