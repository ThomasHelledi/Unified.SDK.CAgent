package tmux

import (
	"context"
	"fmt"
	"strings"
)

// TmuxPane represents a pane within a tmux window.
type TmuxPane struct {
	ID     string `json:"id"`
	Index  int    `json:"index"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Active bool   `json:"active"`
	Title  string `json:"title"`
}

// CreatePane splits the current window in the given session, creating a new
// pane. The new pane's metadata is returned.
func (c *Controller) CreatePane(ctx context.Context, sessionName string) (TmuxPane, error) {
	if sessionName == "" {
		return TmuxPane{}, fmt.Errorf("session name cannot be empty")
	}

	format := "#{pane_id}\t#{pane_index}\t#{pane_width}\t#{pane_height}\t#{pane_active}\t#{pane_title}"
	output, err := c.runTmux(ctx,
		"split-window", "-t", sessionName,
		"-P", "-F", format,
	)
	if err != nil {
		return TmuxPane{}, fmt.Errorf("failed to create pane in session %q: %w", sessionName, err)
	}

	pane, err := parsePaneLine(strings.TrimSpace(output))
	if err != nil {
		return TmuxPane{}, fmt.Errorf("failed to parse new pane info: %w", err)
	}
	return pane, nil
}

// ListPanes returns all panes in the given session.
func (c *Controller) ListPanes(ctx context.Context, sessionName string) ([]TmuxPane, error) {
	if sessionName == "" {
		return nil, fmt.Errorf("session name cannot be empty")
	}

	format := "#{pane_id}\t#{pane_index}\t#{pane_width}\t#{pane_height}\t#{pane_active}\t#{pane_title}"
	output, err := c.runTmux(ctx,
		"list-panes", "-t", sessionName, "-F", format,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list panes for session %q: %w", sessionName, err)
	}

	var panes []TmuxPane
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		pane, err := parsePaneLine(line)
		if err != nil {
			continue
		}
		panes = append(panes, pane)
	}
	return panes, nil
}

// ResizePane resizes a pane to the given width and height. The paneID should
// be a tmux pane identifier like "%0".
func (c *Controller) ResizePane(ctx context.Context, paneID string, width, height int) error {
	if paneID == "" {
		return fmt.Errorf("pane ID cannot be empty")
	}

	if width > 0 {
		if _, err := c.runTmux(ctx, "resize-pane", "-t", paneID, "-x", fmt.Sprintf("%d", width)); err != nil {
			return fmt.Errorf("failed to resize pane %q width: %w", paneID, err)
		}
	}
	if height > 0 {
		if _, err := c.runTmux(ctx, "resize-pane", "-t", paneID, "-y", fmt.Sprintf("%d", height)); err != nil {
			return fmt.Errorf("failed to resize pane %q height: %w", paneID, err)
		}
	}
	return nil
}

// parsePaneLine parses a single tab-separated pane line from tmux output.
func parsePaneLine(line string) (TmuxPane, error) {
	parts := strings.SplitN(line, "\t", 6)
	if len(parts) < 6 {
		return TmuxPane{}, fmt.Errorf("expected 6 fields, got %d: %q", len(parts), line)
	}

	return TmuxPane{
		ID:     parts[0],
		Index:  parseInt(parts[1]),
		Width:  parseInt(parts[2]),
		Height: parseInt(parts[3]),
		Active: parts[4] == "1",
		Title:  parts[5],
	}, nil
}
