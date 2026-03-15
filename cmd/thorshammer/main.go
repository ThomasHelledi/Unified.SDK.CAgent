package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker-agent/pkg/terminal"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var (
		port      int
		shell     string
		agentsDir string
	)

	cmd := &cobra.Command{
		Use:   "thorshammer",
		Short: "ThorsHammer — Unified AI Terminal Orchestration",
		Long: `ThorsHammer manages persistent terminal sessions for LLM agents.
No tmux dependency — uses our own terminal session manager with piped I/O,
ring buffer capture, and REST API for session management.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), port, shell, agentsDir)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8081, "HTTP server port")
	cmd.Flags().StringVar(&shell, "shell", "/bin/zsh", "Shell binary for terminal sessions")
	cmd.Flags().StringVar(&agentsDir, "agents-dir", ".agents", "Directory containing agent YAML definitions")

	return cmd
}

func run(ctx context.Context, port int, shell, agentsDir string) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Initialize our terminal session manager (no tmux needed).
	mgr := terminal.NewManager(shell, 500)
	defer mgr.StopAll()

	// Set up Echo HTTP server.
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// Health endpoint.
	e.GET("/api/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status": "ok",
			"name":   "thorshammer",
		})
	})

	// Status endpoint.
	e.GET("/api/status", func(c echo.Context) error {
		sessions := mgr.ListSessions()
		return c.JSON(http.StatusOK, map[string]any{
			"shell":           shell,
			"agents_dir":      agentsDir,
			"active_sessions": mgr.ActiveCount(),
			"total_sessions":  len(sessions),
		})
	})

	// Session management routes (replaces tmux routes).
	registerSessionRoutes(e, mgr)

	// Start the HTTP server.
	addr := fmt.Sprintf(":%d", port)
	slog.Info("starting ThorsHammer server", "addr", addr, "shell", shell, "agents_dir", agentsDir)

	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down server")
	return e.Close()
}

// registerSessionRoutes sets up REST API for terminal session management.
// Routes kept under /api/tmux/ for backward compatibility with C# service.
func registerSessionRoutes(e *echo.Echo, mgr *terminal.Manager) {
	g := e.Group("/api/tmux")

	// POST /api/tmux/sessions — create new terminal session
	g.POST("/sessions", func(c echo.Context) error {
		var req struct {
			Name    string `json:"name"`
			WorkDir string `json:"work_dir,omitempty"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if req.Name == "" {
			req.Name = "session"
		}

		session, err := mgr.CreateSession(c.Request().Context(), req.Name, req.WorkDir, nil)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		return c.JSON(http.StatusCreated, map[string]string{
			"id":   session.ID,
			"name": session.Name,
		})
	})

	// GET /api/tmux/sessions — list sessions
	g.GET("/sessions", func(c echo.Context) error {
		sessions := mgr.ListSessions()

		type sessionInfo struct {
			ID        string                 `json:"id"`
			Name      string                 `json:"name"`
			State     terminal.SessionState  `json:"state"`
			ExitCode  int                    `json:"exit_code"`
			Lines     int                    `json:"output_lines"`
			CreatedAt string                 `json:"created_at"`
		}

		result := make([]sessionInfo, len(sessions))
		for i, s := range sessions {
			result[i] = sessionInfo{
				ID:        s.ID,
				Name:      s.Name,
				State:     s.State,
				ExitCode:  s.ExitCode,
				Lines:     s.OutputCount(),
				CreatedAt: s.CreatedAt.Format("2006-01-02T15:04:05Z"),
			}
		}
		return c.JSON(http.StatusOK, result)
	})

	// GET /api/tmux/sessions/:id — get session detail
	g.GET("/sessions/:id", func(c echo.Context) error {
		session := mgr.GetSession(c.Param("id"))
		if session == nil {
			return echo.NewHTTPError(http.StatusNotFound, "session not found")
		}
		return c.JSON(http.StatusOK, session)
	})

	// DELETE /api/tmux/sessions/:id — stop and delete session
	g.DELETE("/sessions/:id", func(c echo.Context) error {
		if err := mgr.DeleteSession(c.Param("id")); err != nil {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "session deleted"})
	})

	// POST /api/tmux/sessions/:id/input — send input to session
	g.POST("/sessions/:id/input", func(c echo.Context) error {
		var req struct {
			Input string `json:"input"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := mgr.SendInput(c.Param("id"), req.Input); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "input sent"})
	})

	// GET /api/tmux/sessions/:id/output — get buffered output
	g.GET("/sessions/:id/output", func(c echo.Context) error {
		session := mgr.GetSession(c.Param("id"))
		if session == nil {
			return echo.NewHTTPError(http.StatusNotFound, "session not found")
		}

		lastN := 50
		if n := c.QueryParam("lastN"); n != "" {
			fmt.Sscanf(n, "%d", &lastN)
		}

		lines := session.Output(lastN)
		return c.JSON(http.StatusOK, map[string]any{
			"session_id": session.ID,
			"state":      session.State,
			"total":      session.OutputCount(),
			"returned":   len(lines),
			"lines":      lines,
		})
	})

	// POST /api/tmux/send-keys — backward compat: send input by target ID
	g.POST("/send-keys", func(c echo.Context) error {
		var req struct {
			Target  string `json:"target"`
			Keys    string `json:"keys"`
			Literal bool   `json:"literal"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := mgr.SendInput(req.Target, req.Keys); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "input sent"})
	})

	// POST /api/tmux/run — run command in new session, return output
	g.POST("/run", func(c echo.Context) error {
		var req struct {
			Command string `json:"command"`
			WorkDir string `json:"work_dir,omitempty"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}

		session, err := mgr.RunCommand(c.Request().Context(), "run", req.Command, req.WorkDir)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}

		lines := session.Output(0)
		return c.JSON(http.StatusOK, map[string]any{
			"session_id": session.ID,
			"exit_code":  session.ExitCode,
			"lines":      lines,
		})
	})
}
