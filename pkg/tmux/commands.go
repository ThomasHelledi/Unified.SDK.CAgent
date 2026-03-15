package tmux

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// SendKeys sends keystrokes to the specified tmux target (session, window, or pane).
// When literal is true, each character in keys is sent individually using the -l flag,
// which is useful for typing text. When false, keys are interpreted as tmux key names
// (e.g., "Enter", "C-c").
func (c *Controller) SendKeys(ctx context.Context, target string, keys string, literal bool) error {
	if target == "" {
		return fmt.Errorf("target cannot be empty")
	}

	args := []string{"send-keys", "-t", target}
	if literal {
		args = append(args, "-l")
	}
	args = append(args, keys)

	_, err := c.runTmux(ctx, args...)
	if err != nil {
		return fmt.Errorf("failed to send keys to %q: %w", target, err)
	}
	return nil
}

// CapturePane captures the current visible content of a pane. The target can
// be a pane ID like "%0" or a session:window.pane reference.
func (c *Controller) CapturePane(ctx context.Context, target string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("target cannot be empty")
	}

	output, err := c.runTmux(ctx,
		"capture-pane", "-t", target,
		"-p",  // print to stdout
		"-J",  // join wrapped lines
	)
	if err != nil {
		return "", fmt.Errorf("failed to capture pane %q: %w", target, err)
	}

	return strings.TrimRight(output, "\n"), nil
}

// RunShell sends a command to the target pane and waits briefly for output,
// then captures the pane content. This is a convenience method for running
// a shell command and retrieving its output.
//
// For long-running commands, prefer SendKeys + CapturePane with your own
// polling/waiting strategy.
func (c *Controller) RunShell(ctx context.Context, target string, command string) (string, error) {
	if target == "" {
		return "", fmt.Errorf("target cannot be empty")
	}
	if command == "" {
		return "", fmt.Errorf("command cannot be empty")
	}

	// Clear the pane first so we capture only new output.
	_, _ = c.runTmux(ctx, "send-keys", "-t", target, "C-l")

	// Small delay to let clear take effect.
	time.Sleep(50 * time.Millisecond)

	// Send the command followed by Enter.
	if err := c.SendKeys(ctx, target, command, true); err != nil {
		return "", err
	}
	if err := c.SendKeys(ctx, target, "Enter", false); err != nil {
		return "", err
	}

	// Wait for output to appear. This is a best-effort delay; callers
	// needing deterministic waits should poll CapturePane themselves.
	time.Sleep(500 * time.Millisecond)

	return c.CapturePane(ctx, target)
}
