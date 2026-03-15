package tmux

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

const (
	// DefaultSocketName is the tmux socket name used by ThorsHammer
	// to avoid conflicts with user tmux sessions.
	DefaultSocketName = "thorshammer"
)

// Controller manages a dedicated tmux server instance. It owns the server
// lifecycle and provides the low-level runTmux helper that all other tmux
// operations build on.
type Controller struct {
	mu         sync.RWMutex
	binaryPath string
	socketName string
	running    bool
	serverCmd  *exec.Cmd
	cancelFn   context.CancelFunc
}

// NewController creates a Controller that will use the given tmux binary
// and the "thorshammer" socket name.
func NewController(binaryPath string) *Controller {
	return &Controller{
		binaryPath: binaryPath,
		socketName: DefaultSocketName,
	}
}

// BinaryPath returns the configured tmux binary path.
func (c *Controller) BinaryPath() string {
	return c.binaryPath
}

// SocketName returns the socket name used by this controller.
func (c *Controller) SocketName() string {
	return c.socketName
}

// Start spawns the tmux server. It creates an initial detached session so
// the server process stays alive. The server is killed when Stop is called.
func (c *Controller) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return nil
	}

	// Verify the tmux binary exists before attempting to start.
	if _, err := exec.LookPath(c.binaryPath); err != nil {
		return fmt.Errorf("tmux binary not found at %q: %w", c.binaryPath, err)
	}

	// Kill any stale tmux server on this socket before starting fresh.
	killCmd := exec.Command(c.binaryPath, "-L", c.socketName, "kill-server")
	_ = killCmd.Run() // ignore error — may not be running

	// Start a detached session named "_init" to bootstrap the server.
	// This keeps the server process alive until we explicitly kill it.
	serverCtx, cancel := context.WithCancel(ctx)
	c.cancelFn = cancel

	cmd := exec.CommandContext(serverCtx, c.binaryPath,
		"-L", c.socketName,
		"new-session", "-d", "-s", "_init",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		cancel()
		return fmt.Errorf("failed to start tmux server: %w (output: %s)", err, string(output))
	}

	c.running = true
	slog.Info("tmux server started", "socket", c.socketName, "binary", c.binaryPath)
	return nil
}

// Stop kills the tmux server and all sessions under it.
func (c *Controller) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.running {
		return nil
	}

	// kill-server terminates all sessions and the server itself.
	//nolint:gosec // binaryPath is user-configured, not tainted input
	cmd := exec.Command(c.binaryPath, "-L", c.socketName, "kill-server")
	if output, err := cmd.CombinedOutput(); err != nil {
		slog.Warn("tmux kill-server returned error (may already be stopped)",
			"error", err, "output", string(output))
	}

	if c.cancelFn != nil {
		c.cancelFn()
		c.cancelFn = nil
	}

	c.running = false
	slog.Info("tmux server stopped", "socket", c.socketName)
	return nil
}

// IsRunning reports whether the tmux server is believed to be running.
func (c *Controller) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// runTmux executes a tmux command with the controller's socket and binary,
// returning the combined stdout/stderr output. This is the single point
// through which every tmux operation passes.
func (c *Controller) runTmux(ctx context.Context, args ...string) (string, error) {
	fullArgs := append([]string{"-L", c.socketName}, args...)
	cmd := exec.CommandContext(ctx, c.binaryPath, fullArgs...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("tmux %v failed: %w (output: %s)", args, err, string(output))
	}
	return string(output), nil
}
