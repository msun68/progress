package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"dockerpulltui/ui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

type progressEvent struct {
	id      string
	status  string
	current int64
	total   int64
}

type pullDone struct{}

type pullErr struct{ err error }

type layerState struct {
	current int64
	total   int64
	status  string
	done    bool
}

type model struct {
	image     string
	layers    map[string]layerState
	order     []string
	pl        *ui.ProgressLine
	ctx       context.Context
	cancel    context.CancelFunc
	msgCh     chan tea.Msg
	cancelled bool
	done      bool
	// hideBar avoids showing the bar for up-to-date pulls
	hideBar     bool
	sawDownload bool
}

func initialModel(image string) model {
	ctx, cancel := context.WithCancel(context.Background())
	label := fmt.Sprintf("Pulling %s", image)
	return model{
		image:  image,
		layers: map[string]layerState{},
		order:  []string{},
		pl:     ui.NewProgressLine(label),
		ctx:    ctx,
		cancel: cancel,
		msgCh:  make(chan tea.Msg, 256),
	}
}

func (m model) Init() tea.Cmd {
	go pullImage(m.ctx, m.image, m.msgCh)
	return tea.Batch(waitForMsg(m.msgCh), m.pl.InitCmd())
}

func waitForMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return pullDone{}
		}
		return msg
	}
}

func pullImage(ctx context.Context, img string, out chan<- tea.Msg) {
	defer close(out)
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		out <- pullErr{err}
		return
	}
	defer cli.Close()

	var opts image.PullOptions
	rc, err := cli.ImagePull(ctx, img, opts)
	if err != nil {
		out <- pullErr{err}
		return
	}
	defer rc.Close()

	dec := json.NewDecoder(rc)
	for {
		var e map[string]any
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, context.Canceled) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "context canceled")) {
				return
			}
			out <- pullErr{err}
			return
		}
		if errStr, ok := e["error"].(string); ok && errStr != "" {
			out <- pullErr{errors.New(errStr)}
			return
		}
		id := ""
		if s, ok := e["id"].(string); ok {
			id = s
		}
		status := ""
		if s, ok := e["status"].(string); ok {
			status = s
		}
		var current, total int64
		if pd, ok := e["progressDetail"].(map[string]any); ok {
			if c, ok := pd["current"].(float64); ok {
				current = int64(c)
			}
			if t, ok := pd["total"].(float64); ok {
				total = int64(t)
			}
		}
		out <- progressEvent{id: id, status: status, current: current, total: total}
	}
	out <- pullDone{}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Let component swallow keys; it emits ui.CancelMsg on Esc/Ctrl-C
		if cmd, handled := m.pl.Update(msg); handled {
			return m, cmd
		}
		return m, nil
	case ui.CancelMsg:
		m.cancelled = true
		if m.cancel != nil {
			m.cancel()
		}
		return m, nil
	case progressEvent:
		lowerStatus := strings.ToLower(msg.status)
		if strings.Contains(lowerStatus, "pulling from") {
			// ignore top-level header
			return m, waitForMsg(m.msgCh)
		}
		if strings.Contains(lowerStatus, "image is up to date") {
			m.hideBar = true
		}
		if strings.Contains(lowerStatus, "download") || strings.Contains(lowerStatus, "extract") {
			m.sawDownload = true
		}
		if msg.id != "" {
			ls := m.layers[msg.id]
			if _, ok := m.layers[msg.id]; !ok {
				m.order = append(m.order, msg.id)
			}
			if msg.total > 0 {
				ls.total = msg.total
			}
			if msg.current > 0 || msg.total == 0 {
				ls.current = msg.current
			}
			if msg.status != "" {
				ls.status = msg.status
			}
			switch msg.status {
			case "Download complete", "Pull complete", "Already exists":
				ls.done = true
				if ls.total > 0 && ls.current < ls.total {
					ls.current = ls.total
				}
			}
			m.layers[msg.id] = ls
		}
		// compute overall
		var sumCurrent, sumTotal int64
		allDone := true
		for _, id := range m.order {
			ls := m.layers[id]
			if !(ls.status == "Pull complete" || ls.status == "Already exists") {
				allDone = false
			}
			if ls.total > 0 {
				sumCurrent += ls.current
				sumTotal += ls.total
			}
		}
		// If all done and we never downloaded anything, hide the bar entirely
		if allDone && !m.sawDownload {
			m.hideBar = true
		}
		var cmds []tea.Cmd
		if sumTotal > 0 {
			pct := float64(sumCurrent) / float64(sumTotal)
			if !allDone && pct >= 0.999 {
				pct = 0.99
			}
			if pct < m.pl.Percent {
				pct = m.pl.Percent
			}
			if c, handled := m.pl.Update(ui.SetPercentMsg{Pct: pct}); handled {
				cmds = append(cmds, c)
			}
		}
		return m, tea.Batch(append(cmds, waitForMsg(m.msgCh))...)
	case pullDone:
		m.done = true
		_, _ = m.pl.Update(ui.DoneMsg{})
		return m, tea.Quit
	case pullErr:
		// Treat context canceled as clean exit
		if errors.Is(msg.err, context.Canceled) || (msg.err != nil && strings.Contains(strings.ToLower(msg.err.Error()), "context canceled")) {
			return m, tea.Quit
		}
		// Print error and quit
		fmt.Printf("Error: %v\n", msg.err)
		return m, tea.Quit
	}
	return m, nil
}

func (m model) View() string {
	if m.cancelled {
		return fmt.Sprintf("Pulling %s...CANCELLED\n", m.image)
	}
	if m.done {
		return fmt.Sprintf("Pulling %s...DONE\n", m.image)
	}
	// If we haven't seen any downloading/extracting yet, or we've determined the image is up to date, hide the bar
	if m.hideBar || !m.sawDownload {
		return fmt.Sprintf("Pulling %s...\n", m.image)
	}
	return m.pl.View()
}

func main() {
	image := "node:20"
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		image = os.Args[1]
	}
	p := tea.NewProgram(initialModel(image))
	if _, err := p.Run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
