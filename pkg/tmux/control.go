package tmux

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ControlEvent represents a raw parsed event from tmux control mode output.
type ControlEvent struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// ControlClient manages a tmux control-mode (-CC) connection to a session.
// It reads the control protocol stream and routes parsed events through the
// event bus.
type ControlClient struct {
	controller *Controller
	session    string

	mu       sync.Mutex
	cmd      *exec.Cmd
	cancelFn context.CancelFunc
	bus      *eventBus
	running  bool
}

// StartControlMode launches a tmux control-mode client attached to the
// given session. Events are parsed in a background goroutine and dispatched
// through the event bus. Call StopControlMode to terminate.
func (c *Controller) StartControlMode(ctx context.Context, sessionName string) (*ControlClient, error) {
	if sessionName == "" {
		return nil, fmt.Errorf("session name cannot be empty")
	}

	if !c.HasSession(ctx, sessionName) {
		return nil, fmt.Errorf("session %q does not exist", sessionName)
	}

	controlCtx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(controlCtx, c.binaryPath,
		"-L", c.socketName,
		"-CC",
		"attach-session", "-t", sessionName,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to start control mode for session %q: %w", sessionName, err)
	}

	client := &ControlClient{
		controller: c,
		session:    sessionName,
		cmd:        cmd,
		cancelFn:   cancel,
		bus:        newEventBus(),
		running:    true,
	}

	// Background goroutine: read control-mode protocol lines and parse them.
	go func() {
		defer func() {
			client.mu.Lock()
			client.running = false
			client.mu.Unlock()
			client.bus.close()
		}()

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if event, ok := parseControlLine(line, sessionName); ok {
				client.bus.publish(event)
			}
		}

		if err := scanner.Err(); err != nil && controlCtx.Err() == nil {
			slog.Warn("control mode scanner error", "session", sessionName, "error", err)
		}

		// Wait for the process to exit.
		_ = cmd.Wait()
	}()

	slog.Info("control mode started", "session", sessionName)
	return client, nil
}

// Subscribe returns a channel that receives events of the specified type
// from this control-mode connection.
func (cc *ControlClient) Subscribe(eventType EventType) <-chan TmuxEvent {
	return cc.bus.Subscribe(eventType)
}

// Stop terminates the control-mode connection.
func (cc *ControlClient) Stop() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if !cc.running {
		return nil
	}

	if cc.cancelFn != nil {
		cc.cancelFn()
	}

	cc.running = false
	slog.Info("control mode stopped", "session", cc.session)
	return nil
}

// IsRunning reports whether the control-mode client is still active.
func (cc *ControlClient) IsRunning() bool {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.running
}

// parseControlLine interprets a single line of tmux control-mode output.
// Control-mode lines begin with "%" and carry structured information about
// tmux state changes.
//
// Protocol reference (partial):
//
//	%output <pane-id> <data>          — pane produced output
//	%begin <time> <num> <flags>       — start of command response block
//	%end <time> <num> <flags>         — end of command response block
//	%session-changed $<id> <name>     — attached session switched
//	%session-created <name>           — new session created (tmux 3.3a+)
//	%window-changed                   — active window changed
func parseControlLine(line string, sessionName string) (TmuxEvent, bool) {
	if !strings.HasPrefix(line, "%") {
		return TmuxEvent{}, false
	}

	now := time.Now()

	// %output %<paneId> <data>
	if strings.HasPrefix(line, "%output ") {
		rest := line[len("%output "):]
		spaceIdx := strings.Index(rest, " ")
		if spaceIdx < 0 {
			return TmuxEvent{
				Type:        EventOutput,
				SessionName: sessionName,
				PaneID:      rest,
				Timestamp:   now,
			}, true
		}
		return TmuxEvent{
			Type:        EventPaneOutput,
			SessionName: sessionName,
			PaneID:      rest[:spaceIdx],
			Data:        rest[spaceIdx+1:],
			Timestamp:   now,
		}, true
	}

	// %session-changed $<id> <name>
	if strings.HasPrefix(line, "%session-changed ") {
		rest := line[len("%session-changed "):]
		parts := strings.SplitN(rest, " ", 2)
		name := sessionName
		if len(parts) >= 2 {
			name = parts[1]
		}
		return TmuxEvent{
			Type:        EventSessionCreated,
			SessionName: name,
			Data:        rest,
			Timestamp:   now,
		}, true
	}

	// %session-created <name> (tmux 3.3a+)
	if strings.HasPrefix(line, "%session-created ") {
		name := strings.TrimSpace(line[len("%session-created "):])
		return TmuxEvent{
			Type:        EventSessionCreated,
			SessionName: name,
			Timestamp:   now,
		}, true
	}

	// %window-changed
	if strings.HasPrefix(line, "%window-changed") {
		return TmuxEvent{
			Type:        EventWindowChanged,
			SessionName: sessionName,
			Data:        strings.TrimSpace(line[len("%window-changed"):]),
			Timestamp:   now,
		}, true
	}

	// %begin / %end — we note them but don't route as distinct events
	// for now. Callers can subscribe to EventOutput for general tracking.

	return TmuxEvent{}, false
}
