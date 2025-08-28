package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"golang.org/x/term"
)

type layerState struct {
	current int64
	total   int64
	status  string
	done    bool
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	val := float64(n)
	i := 0
	for val >= 1024 && i < len(units)-1 {
		val /= 1024
		i++
	}
	return fmt.Sprintf("%.2f%s", val, units[i])
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

func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

func render(w io.Writer, image string, order []string, layers map[string]layerState, lastPct *float64) {
	// Compute overall from summed bytes where totals are known
	var sumCurrent, sumTotal int64
	allDone := true
	for _, id := range order {
		if ls, ok := layers[id]; ok {
			if !(ls.status == "Pull complete" || ls.status == "Already exists") {
				allDone = false
			}
			if ls.total > 0 {
				sumCurrent += ls.current
				sumTotal += ls.total
			}
		}
	}
	pct := 0.0
	if sumTotal > 0 {
		pct = float64(sumCurrent) / float64(sumTotal)
	}
	// Don't ever show 100% until all layers report done/exist
	if !allDone && pct >= 0.999 {
		pct = 0.99
	}
	// Clamp to never decrease vs last printed percent
	if lastPct != nil {
		if pct < *lastPct {
			pct = *lastPct
		} else {
			*lastPct = pct
		}
	}
	line := fmt.Sprintf("Pulling %s...[%s] %3.0f%%", image, asciiBar(pct, 40), pct*100)
	// Carriage return, clear line, then print without newline
	fmt.Fprintf(w, "\r\033[2K%s", line)
}

func main() {
	imageRef := "node:20"
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		imageRef = os.Args[1]
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Put terminal into raw mode to prevent Enter from inserting newlines
	// Try stdin first; if not a TTY, fall back to /dev/tty
	var (
		oldState *term.State
		ttyFile  *os.File
		ttyFd    = int(os.Stdin.Fd())
	)
	if !term.IsTerminal(ttyFd) {
		if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
			ttyFile = f
			ttyFd = int(f.Fd())
		}
	}
	if term.IsTerminal(ttyFd) {
		if st, err := term.MakeRaw(ttyFd); err == nil {
			oldState = st
		}
	}
	// Track cancellation triggered by keyboard (ESC/Ctrl-C)
	var cancelled atomic.Bool
	// Drain keystrokes from the same TTY we set raw on; ESC/Ctrl-C cancel
	inputFile := os.Stdin
	if ttyFile != nil {
		inputFile = ttyFile
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				n, _ := inputFile.Read(buf)
				for i := 0; i < n; i++ {
					if buf[i] == 27 /* ESC */ || buf[i] == 3 /* Ctrl-C */ {
						cancelled.Store(true)
						cancel()
						return
					}
				}
			}
		}
	}()
	// Ensure terminal is restored and cursor shown on any exit path
	defer func() {
		if oldState != nil {
			_ = term.Restore(ttyFd, oldState)
		}
		if ttyFile != nil {
			_ = ttyFile.Close()
		}
		fmt.Print("\033[?25h")
	}()

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer cli.Close()

	opts := image.PullOptions{}
	rc, err := cli.ImagePull(ctx, imageRef, opts)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer rc.Close()
	// Close stream on cancel to unblock decoder
	go func() {
		<-ctx.Done()
		_ = rc.Close()
	}()

	dec := json.NewDecoder(rc)
	layers := map[string]layerState{}
	var order []string
	lastPct := 0.0

	// Hide cursor for single-line updates
	out := io.Writer(os.Stdout)
	if ttyFile != nil {
		out = ttyFile
	}
	fmt.Fprint(out, "\033[?25l")

	finalize := func() {
		// Overwrite the progress line with DONE/CANCELLED and end with CRLF
		if cancelled.Load() {
			fmt.Fprintf(out, "\r\033[2KPulling %s...CANCELLED\r\n", imageRef)
		} else {
			fmt.Fprintf(out, "\r\033[2KPulling %s...DONE\r\n", imageRef)
		}
		// Show cursor again
		fmt.Fprint(out, "\033[?25h")
	}

	exitNow := func() {
		cancel()
		_ = rc.Close()
	}

	for {
		// If user canceled, finalize immediately and exit
		if cancelled.Load() {
			finalize()
			exitNow()
			break
		}
		var e map[string]any
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, context.Canceled) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "context canceled")) {
				finalize()
				break
			}
			fmt.Println("Error:", err)
			os.Exit(1)
		}

		if errStr, ok := e["error"].(string); ok && errStr != "" {
			fmt.Println("Error:", errStr)
			os.Exit(1)
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

		// Filter out any 'Pulling from ...' lines and rely on Overall
		lowerStatus := strings.ToLower(status)
		if strings.Contains(lowerStatus, "pulling from") {
			continue
		}

		// Handle top-level final statuses
		if id == "" {
			if strings.Contains(lowerStatus, "digest:") || strings.Contains(lowerStatus, "downloaded newer image") || strings.Contains(lowerStatus, "image is up to date") {
				// force overall to 100% for final status (e.g., already up to date)
				lastPct = 1.0
				render(out, imageRef, order, layers, &lastPct)
				finalize()
				exitNow()
				break
			}
			// render on other header-like statuses too
			render(out, imageRef, order, layers, &lastPct)
			continue
		}

		ls := layers[id]
		if _, ok := layers[id]; !ok {
			order = append(order, id)
		}
		if total > 0 {
			ls.total = total
		}
		if current > 0 || total == 0 {
			ls.current = current
		}
		if status != "" {
			ls.status = status
		}
		switch status {
		case "Download complete", "Pull complete", "Already exists":
			ls.done = true
			if ls.total > 0 && ls.current < ls.total {
				ls.current = ls.total
			}
		}
		layers[id] = ls

		// Immediate exit if all layers done/exist
		if len(layers) > 0 {
			allDone := true
			for _, s := range layers {
				if !(s.status == "Pull complete" || s.status == "Already exists") {
					allDone = false
					break
				}
			}
			if allDone {
				// final render before exit
				render(out, imageRef, order, layers, &lastPct)
				finalize()
				exitNow()
				break
			}
		}

		// Render full frame in-place each event
		render(out, imageRef, order, layers, &lastPct)
	}
}
