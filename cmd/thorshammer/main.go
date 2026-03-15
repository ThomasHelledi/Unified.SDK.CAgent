package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/docker/docker-agent/pkg/tmux"
	agenttools "github.com/docker/docker-agent/pkg/tools"
	tmuxtools "github.com/docker/docker-agent/pkg/tools/tmux"
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
		tmuxPath  string
		agentsDir string
	)

	cmd := &cobra.Command{
		Use:   "thorshammer",
		Short: "ThorsHammer — CAgent server with tmux control capabilities",
		Long: `ThorsHammer integrates the CAgent SDK with tmux to provide
LLM-callable tools for terminal session management. It runs a lightweight
HTTP server exposing tmux operations as both REST endpoints and agent tools.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), port, tmuxPath, agentsDir)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8081, "HTTP server port")
	cmd.Flags().StringVar(&tmuxPath, "tmux-path", "tmux", "Path to tmux binary")
	cmd.Flags().StringVar(&agentsDir, "agents-dir", ".agents", "Directory containing agent definitions")

	return cmd
}

func run(ctx context.Context, port int, tmuxPath, agentsDir string) error {
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Initialize tmux controller.
	controller := tmux.NewController(tmuxPath)

	slog.Info("starting tmux server", "binary", tmuxPath)
	if err := controller.Start(ctx); err != nil {
		return fmt.Errorf("failed to start tmux server: %w", err)
	}
	defer func() {
		slog.Info("shutting down tmux server")
		_ = controller.Stop()
	}()

	// Create the tmux tool set for agent registration.
	toolSet := tmuxtools.NewTmuxToolSet(controller)

	// Set up Echo HTTP server.
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// Health and status endpoints.
	e.GET("/api/ping", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{
			"status": "ok",
			"name":   "thorshammer",
		})
	})

	e.GET("/api/status", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]any{
			"tmux_running": controller.IsRunning(),
			"socket":       controller.SocketName(),
			"binary":       controller.BinaryPath(),
			"agents_dir":   agentsDir,
		})
	})

	// Tmux REST endpoints.
	tmuxGroup := e.Group("/api/tmux")
	registerTmuxRoutes(tmuxGroup, controller)

	// Tool listing endpoint (for agent discovery).
	e.GET("/api/tools", func(c echo.Context) error {
		toolList, err := toolSet.Tools(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError,
				fmt.Sprintf("failed to list tools: %v", err))
		}
		return c.JSON(http.StatusOK, toolList)
	})

	// Tool execution endpoint (for agent invocation).
	e.POST("/api/tools/execute", func(c echo.Context) error {
		var req struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest,
				fmt.Sprintf("invalid request body: %v", err))
		}

		toolList, err := toolSet.Tools(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError,
				fmt.Sprintf("failed to list tools: %v", err))
		}

		for _, tool := range toolList {
			if tool.Name != req.Name {
				continue
			}

			tc := agenttools.ToolCall{
				Type: "function",
				Function: agenttools.FunctionCall{
					Name:      req.Name,
					Arguments: req.Arguments,
				},
			}

			result, err := tool.Handler(c.Request().Context(), tc)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError,
					fmt.Sprintf("tool execution failed: %v", err))
			}
			return c.JSON(http.StatusOK, result)
		}

		return echo.NewHTTPError(http.StatusNotFound,
			fmt.Sprintf("tool %q not found", req.Name))
	})

	// Start the HTTP server.
	addr := fmt.Sprintf(":%d", port)
	slog.Info("starting ThorsHammer server", "addr", addr, "agents_dir", agentsDir)

	go func() {
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			cancel()
		}
	}()

	// Wait for shutdown signal.
	<-ctx.Done()
	slog.Info("shutting down server")

	return e.Close()
}

// registerTmuxRoutes sets up the REST API routes for direct tmux operations.
func registerTmuxRoutes(g *echo.Group, ctrl *tmux.Controller) {
	// Sessions
	g.POST("/sessions", func(c echo.Context) error {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		id, err := ctrl.CreateSession(c.Request().Context(), req.Name)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusCreated, map[string]string{
			"name": req.Name,
			"id":   id,
		})
	})

	g.GET("/sessions", func(c echo.Context) error {
		sessions, err := ctrl.ListSessions(c.Request().Context())
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, sessions)
	})

	g.DELETE("/sessions/:name", func(c echo.Context) error {
		if err := ctrl.KillSession(c.Request().Context(), c.Param("name")); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "session killed"})
	})

	// Panes
	g.GET("/sessions/:name/panes", func(c echo.Context) error {
		panes, err := ctrl.ListPanes(c.Request().Context(), c.Param("name"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, panes)
	})

	g.POST("/sessions/:name/panes", func(c echo.Context) error {
		pane, err := ctrl.CreatePane(c.Request().Context(), c.Param("name"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusCreated, pane)
	})

	g.POST("/sessions/:name/panes/:pane/resize", func(c echo.Context) error {
		var req struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := ctrl.ResizePane(c.Request().Context(), c.Param("pane"), req.Width, req.Height); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "pane resized"})
	})

	// Commands
	g.POST("/send-keys", func(c echo.Context) error {
		var req struct {
			Target  string `json:"target"`
			Keys    string `json:"keys"`
			Literal bool   `json:"literal"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		if err := ctrl.SendKeys(c.Request().Context(), req.Target, req.Keys, req.Literal); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"message": "keys sent"})
	})

	g.POST("/capture", func(c echo.Context) error {
		var req struct {
			Target string `json:"target"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		content, err := ctrl.CapturePane(c.Request().Context(), req.Target)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"content": content})
	})

	g.POST("/run", func(c echo.Context) error {
		var req struct {
			Target  string `json:"target"`
			Command string `json:"command"`
		}
		if err := c.Bind(&req); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, err.Error())
		}
		output, err := ctrl.RunShell(c.Request().Context(), req.Target, req.Command)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, map[string]string{"output": output})
	})

	// Control mode
	g.POST("/sessions/:name/control", func(c echo.Context) error {
		client, err := ctrl.StartControlMode(c.Request().Context(), c.Param("name"))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		// For REST, we return immediately. The control client runs in the background.
		// A WebSocket upgrade would be more appropriate for streaming events.
		_ = client
		return c.JSON(http.StatusOK, map[string]string{
			"message": "control mode started",
			"session": c.Param("name"),
		})
	})
}
