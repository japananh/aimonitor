package cli

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/daemon"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Control the aimonitor background daemon",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "start",
			Short: "Start the daemon (stubbed — use `aimonitor daemon run` directly under launchd/systemd)",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon start") },
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the daemon (stubbed)",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon stop") },
		},
		&cobra.Command{
			Use:   "restart",
			Short: "Restart the daemon (stubbed)",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon restart") },
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report the daemon's status (stubbed)",
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented("daemon status") },
		},
		&cobra.Command{
			Use:   "run",
			Short: "Run the daemon in the foreground (entry point for launchd/systemd)",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runDaemon(cmd)
			},
		},
	)
	return cmd
}

// runDaemon is `aimonitor daemon run`. It opens the store, looks up the
// Claude provider, loads the user config, and blocks in daemon.Server.Run
// until SIGINT/SIGTERM. The CLI command stays thin; orchestration lives
// in the daemon package.
func runDaemon(cmd *cobra.Command) error {
	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	s, err := openStore()
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	p, err := claudeProvider()
	if err != nil {
		return err
	}

	srv, err := daemon.NewServer(daemon.ServerConfig{
		Store:    s,
		Provider: p,
		Config:   cfg,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fmt.Fprintln(cmd.OutOrStdout(), "aimonitor daemon running; Ctrl-C to stop")
	if err := srv.Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")

	// Compile-time signal that the imports above are used.
	_ = provider.Provider(nil)
	_ = (*store.Store)(nil)
	return nil
}
