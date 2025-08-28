package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/progress"
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
	Bar     progress.Model
	Percent float64
	Done    bool
	Width   int
}

// NewProgressLine creates a new progress line with a label.
func NewProgressLine(label string) *ProgressLine {
	pl := &ProgressLine{
		Label:   label,
		Bar:     progress.New(progress.WithDefaultGradient()),
		Percent: 0,
		Width:   40,
	}
	return pl
}

// InitCmd returns a tick command you can use in your parent model to drive animations.
func (p *ProgressLine) InitCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*100, func(time.Time) tea.Msg { return progress.FrameMsg{} })
}

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
		return p.Bar.SetPercent(p.Percent), true
	case DoneMsg:
		p.Percent = 1
		p.Done = true
		return p.Bar.SetPercent(1), true
	case progress.FrameMsg:
		var cmd tea.Cmd
		var nxt tea.Model
		nxt, cmd = p.Bar.Update(m)
		if b, ok := nxt.(progress.Model); ok {
			p.Bar = b
		}
		return cmd, true
	}
	return nil, false
}

// View returns the single-line string for this component.
func (p *ProgressLine) View() string {
	return fmt.Sprintf("%s...[%s] %3.0f%%", p.Label, p.Bar.ViewAs(p.Percent), p.Percent*100)
}
