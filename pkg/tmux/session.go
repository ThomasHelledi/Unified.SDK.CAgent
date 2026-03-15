package tmux

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TmuxSession represents a tmux session with its metadata.
type TmuxSession struct {
	Name     string    `json:"name"`
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	Attached bool      `json:"attached"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
}

// CreateSession creates a new detached tmux session with the given name
// and returns its session ID.
func (c *Controller) CreateSession(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("session name cannot be empty")
	}

	// Create detached session. Print the session ID after creation.
	output, err := c.runTmux(ctx,
		"new-session", "-d", "-s", name,
		"-P", "-F", "#{session_id}",
	)
	if err != nil {
		return "", fmt.Errorf("failed to create session %q: %w", name, err)
	}

	return strings.TrimSpace(output), nil
}

// ListSessions returns all tmux sessions managed by this controller.
func (c *Controller) ListSessions(ctx context.Context) ([]TmuxSession, error) {
	format := "#{session_name}\t#{session_id}\t#{session_created}\t#{session_attached}\t#{session_width}\t#{session_height}"
	output, err := c.runTmux(ctx, "list-sessions", "-F", format)
	if err != nil {
		// No sessions is not an error condition.
		if strings.Contains(err.Error(), "no server running") ||
			strings.Contains(err.Error(), "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	var sessions []TmuxSession
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}

		var created time.Time
		if ts, err := parseTimestamp(parts[2]); err == nil {
			created = ts
		}

		sessions = append(sessions, TmuxSession{
			Name:     parts[0],
			ID:       parts[1],
			Created:  created,
			Attached: parts[3] == "1",
			Width:    parseInt(parts[4]),
			Height:   parseInt(parts[5]),
		})
	}

	return sessions, nil
}

// KillSession destroys a tmux session by name.
func (c *Controller) KillSession(ctx context.Context, name string) error {
	if name == "" {
		return fmt.Errorf("session name cannot be empty")
	}

	_, err := c.runTmux(ctx, "kill-session", "-t", name)
	if err != nil {
		return fmt.Errorf("failed to kill session %q: %w", name, err)
	}
	return nil
}

// HasSession checks whether a session with the given name exists.
func (c *Controller) HasSession(ctx context.Context, name string) bool {
	_, err := c.runTmux(ctx, "has-session", "-t", name)
	return err == nil
}

// parseTimestamp attempts to parse a Unix timestamp string from tmux.
func parseTimestamp(s string) (time.Time, error) {
	epoch := parseInt(s)
	if epoch == 0 {
		return time.Time{}, fmt.Errorf("invalid timestamp: %s", s)
	}
	return time.Unix(int64(epoch), 0), nil
}

// parseInt parses a string to int, returning 0 on failure.
func parseInt(s string) int {
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			break
		}
		n = n*10 + int(ch-'0')
	}
	return n
}
