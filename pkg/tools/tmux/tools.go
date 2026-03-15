package tmux

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	tmuxpkg "github.com/docker/docker-agent/pkg/tmux"
	"github.com/docker/docker-agent/pkg/tools"
)

const (
	ToolNameRunCommand = "tmux_run_command"
	ToolNameSendKeys   = "tmux_send_keys"
	ToolNameCapture    = "tmux_capture"
	ToolNameSplit      = "tmux_split"
	ToolNameList       = "tmux_list"
)

// TmuxToolSet exposes tmux operations as LLM-callable agent tools. It
// implements the tools.ToolSet interface so it can be registered with the
// CAgent runtime.
type TmuxToolSet struct {
	controller *tmuxpkg.Controller
}

// Verify interface compliance at compile time.
var (
	_ tools.ToolSet   = (*TmuxToolSet)(nil)
	_ tools.Describer = (*TmuxToolSet)(nil)
)

// NewTmuxToolSet creates a tool set backed by the given tmux controller.
func NewTmuxToolSet(controller *tmuxpkg.Controller) *TmuxToolSet {
	return &TmuxToolSet{controller: controller}
}

// Describe returns a short, user-visible description of this toolset.
func (t *TmuxToolSet) Describe() string {
	return "tmux(socket=" + t.controller.SocketName() + ")"
}

// --- Parameter structs -------------------------------------------------------

// RunCommandArgs are the parameters for tmux_run_command.
type RunCommandArgs struct {
	Target  string `json:"target" jsonschema:"The tmux target (session:window.pane) to run the command in"`
	Command string `json:"command" jsonschema:"The shell command to execute"`
}

// SendKeysArgs are the parameters for tmux_send_keys.
type SendKeysArgs struct {
	Target  string `json:"target" jsonschema:"The tmux target (session:window.pane) to send keys to"`
	Keys    string `json:"keys" jsonschema:"The keystrokes to send. Use literal text or tmux key names like Enter, C-c, Escape"`
	Literal bool   `json:"literal,omitempty" jsonschema:"When true, keys are sent as literal text. When false, keys are interpreted as tmux key names"`
}

// CaptureArgs are the parameters for tmux_capture.
type CaptureArgs struct {
	Target string `json:"target" jsonschema:"The tmux target (session:window.pane) to capture content from"`
}

// SplitArgs are the parameters for tmux_split.
type SplitArgs struct {
	Session    string `json:"session" jsonschema:"The session name in which to split a pane"`
	Horizontal bool   `json:"horizontal,omitempty" jsonschema:"When true, split horizontally (top/bottom). Default is vertical (left/right)"`
}

// ListArgs are the parameters for tmux_list.
type ListArgs struct {
	Session string `json:"session,omitempty" jsonschema:"Optional session name to list panes for. If empty, lists all sessions"`
}

// --- Tool registration -------------------------------------------------------

// Tools returns the list of tmux tools available for LLM use.
func (t *TmuxToolSet) Tools(_ context.Context) ([]tools.Tool, error) {
	return []tools.Tool{
		{
			Name:        ToolNameRunCommand,
			Category:    "tmux",
			Description: "Execute a command in a tmux pane, wait for output, and return the captured pane content. Best for short-lived commands where you want to see the result.",
			Parameters:  tools.MustSchemaFor[RunCommandArgs](),
			Handler:     tools.NewHandler(t.handleRunCommand),
			Annotations: tools.ToolAnnotations{
				Title: "Run Command in Tmux",
			},
		},
		{
			Name:        ToolNameSendKeys,
			Category:    "tmux",
			Description: "Send keystrokes to a tmux pane. Use literal mode for typing text, or non-literal mode for special keys like Enter, C-c, Escape. Useful for interactive programs.",
			Parameters:  tools.MustSchemaFor[SendKeysArgs](),
			Handler:     tools.NewHandler(t.handleSendKeys),
			Annotations: tools.ToolAnnotations{
				Title: "Send Keys to Tmux Pane",
			},
		},
		{
			Name:        ToolNameCapture,
			Category:    "tmux",
			Description: "Capture and return the current visible content of a tmux pane. Returns the text exactly as displayed on screen.",
			Parameters:  tools.MustSchemaFor[CaptureArgs](),
			Handler:     tools.NewHandler(t.handleCapture),
			Annotations: tools.ToolAnnotations{
				Title:        "Capture Tmux Pane Content",
				ReadOnlyHint: true,
			},
		},
		{
			Name:        ToolNameSplit,
			Category:    "tmux",
			Description: "Split a tmux window to create a new pane. Returns the new pane's ID and dimensions.",
			Parameters:  tools.MustSchemaFor[SplitArgs](),
			Handler:     tools.NewHandler(t.handleSplit),
			Annotations: tools.ToolAnnotations{
				Title: "Split Tmux Pane",
			},
		},
		{
			Name:        ToolNameList,
			Category:    "tmux",
			Description: "List all tmux sessions and their panes with content previews. If a session name is provided, lists only panes for that session.",
			Parameters:  tools.MustSchemaFor[ListArgs](),
			Handler:     tools.NewHandler(t.handleList),
			Annotations: tools.ToolAnnotations{
				Title:        "List Tmux Sessions and Panes",
				ReadOnlyHint: true,
			},
		},
	}, nil
}

// --- Handlers ----------------------------------------------------------------

func (t *TmuxToolSet) handleRunCommand(ctx context.Context, args RunCommandArgs) (*tools.ToolCallResult, error) {
	if args.Target == "" {
		return tools.ResultError("target is required"), nil
	}
	if args.Command == "" {
		return tools.ResultError("command is required"), nil
	}

	output, err := t.controller.RunShell(ctx, args.Target, args.Command)
	if err != nil {
		return tools.ResultError(fmt.Sprintf("failed to run command: %v", err)), nil
	}

	return tools.ResultSuccess(output), nil
}

func (t *TmuxToolSet) handleSendKeys(ctx context.Context, args SendKeysArgs) (*tools.ToolCallResult, error) {
	if args.Target == "" {
		return tools.ResultError("target is required"), nil
	}
	if args.Keys == "" {
		return tools.ResultError("keys is required"), nil
	}

	if err := t.controller.SendKeys(ctx, args.Target, args.Keys, args.Literal); err != nil {
		return tools.ResultError(fmt.Sprintf("failed to send keys: %v", err)), nil
	}

	return tools.ResultSuccess(fmt.Sprintf("Keys sent to %s", args.Target)), nil
}

func (t *TmuxToolSet) handleCapture(ctx context.Context, args CaptureArgs) (*tools.ToolCallResult, error) {
	if args.Target == "" {
		return tools.ResultError("target is required"), nil
	}

	content, err := t.controller.CapturePane(ctx, args.Target)
	if err != nil {
		return tools.ResultError(fmt.Sprintf("failed to capture pane: %v", err)), nil
	}

	return tools.ResultSuccess(content), nil
}

func (t *TmuxToolSet) handleSplit(ctx context.Context, args SplitArgs) (*tools.ToolCallResult, error) {
	if args.Session == "" {
		return tools.ResultError("session is required"), nil
	}

	pane, err := t.controller.CreatePane(ctx, args.Session)
	if err != nil {
		return tools.ResultError(fmt.Sprintf("failed to split pane: %v", err)), nil
	}

	return tools.ResultJSON(pane), nil
}

func (t *TmuxToolSet) handleList(ctx context.Context, args ListArgs) (*tools.ToolCallResult, error) {
	// If a session is specified, list panes for that session with previews.
	if args.Session != "" {
		panes, err := t.controller.ListPanes(ctx, args.Session)
		if err != nil {
			return tools.ResultError(fmt.Sprintf("failed to list panes: %v", err)), nil
		}

		type paneWithPreview struct {
			tmuxpkg.TmuxPane
			Preview string `json:"preview"`
		}

		var results []paneWithPreview
		for _, p := range panes {
			preview, _ := t.controller.CapturePane(ctx, p.ID)
			// Truncate preview to last 10 lines for readability.
			preview = truncateLines(preview, 10)
			results = append(results, paneWithPreview{
				TmuxPane: p,
				Preview:  preview,
			})
		}

		data, err := json.Marshal(results)
		if err != nil {
			return tools.ResultError(fmt.Sprintf("failed to marshal pane list: %v", err)), nil
		}
		return tools.ResultSuccess(string(data)), nil
	}

	// No session specified: list all sessions.
	sessions, err := t.controller.ListSessions(ctx)
	if err != nil {
		return tools.ResultError(fmt.Sprintf("failed to list sessions: %v", err)), nil
	}

	if len(sessions) == 0 {
		return tools.ResultSuccess("No active tmux sessions"), nil
	}

	data, err := json.Marshal(sessions)
	if err != nil {
		return tools.ResultError(fmt.Sprintf("failed to marshal session list: %v", err)), nil
	}
	return tools.ResultSuccess(string(data)), nil
}

// truncateLines returns the last n lines of text.
func truncateLines(text string, n int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= n {
		return text
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
