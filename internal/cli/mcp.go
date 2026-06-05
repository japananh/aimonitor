package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/mcpserver"
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP server: Slack + ClickUp tools for Claude Code",
		Long: `aimonitor doubles as an MCP server exposing Slack and ClickUp tools to
Claude Code over stdio.

Setup:
  aimonitor mcp connect slack     # migrate claude-bar's token, or paste one
  aimonitor mcp connect clickup
  aimonitor mcp register          # add the server to Claude Code (~/.claude.json)

Then restart your Claude session; the tools appear as mcp__aimonitor__*.

Per-service config (settings):
  mcp.slack.enabled / mcp.clickup.enabled       (default true)
  mcp.slack.read_only / mcp.clickup.read_only   (default false; hides write tools)
  mcp.disabled_tools                            (comma-separated tool names)`,
	}
	cmd.AddCommand(
		newMCPServeCmd(),
		newMCPConnectCmd(),
		newMCPDisconnectCmd(),
		newMCPStatusCmd(),
		newMCPRegisterCmd(),
	)
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "serve",
		Short:  "Run the stdio MCP server (spawned by Claude Code; not for interactive use)",
		Hidden: true, // users interact via connect/register; Claude Code runs this
		Args:   cobra.NoArgs,
		// stdout belongs to the JSON-RPC stream; never let cobra print
		// usage there, and keep error echo on stderr only.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openConfigStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			cfg, err := mcpserver.LoadConfig(cmd.Context(), s)
			if err != nil {
				return fmt.Errorf("load mcp config: %w", err)
			}
			creds, err := mcpserver.NewCredStore()
			if err != nil {
				return err
			}
			return mcpserver.Serve(cmd.Context(), cfg, creds)
		},
	}
}

func newMCPConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect <slack|clickup>",
		Short: "Connect an integration (migrates claude-bar's token, or paste one)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := mcpserver.ParseService(args[0])
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			creds, err := mcpserver.NewCredStore()
			if err != nil {
				return err
			}
			verify := mcpserver.Verifier(svc)

			// 1) Try migrating claude-bar's entry (read-only; theirs stays).
			ident, err := creds.MigrateFromClaudeBar(ctx, svc, verify)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "claude-bar migration unavailable: %v\n", err)
			}
			if ident != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Migrated %s token from claude-bar — verified as %s.\n", svc, ident)
				return nil
			}

			// 2) Fall back to pasting a token.
			hint := "xoxp-… user token (Slack → your app or claude-bar's token)"
			if svc == mcpserver.ServiceClickUp {
				hint = "pk_… personal token (ClickUp → Settings → Apps)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "No claude-bar token found for %s.\nPaste your %s and press Enter:\n> ", svc, hint)
			rd := bufio.NewReader(cmd.InOrStdin())
			line, err := rd.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read token: %w", err)
			}
			token := strings.TrimSpace(line)
			if token == "" {
				return fmt.Errorf("no token entered")
			}
			ident, err = verify(ctx, token)
			if err != nil {
				return fmt.Errorf("token verification failed: %w", err)
			}
			if err := creds.Store(svc, token); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Connected %s — verified as %s.\n", svc, ident)
			return nil
		},
	}
}

func newMCPDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <slack|clickup>",
		Short: "Remove an integration's stored token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := mcpserver.ParseService(args[0])
			if err != nil {
				return err
			}
			creds, err := mcpserver.NewCredStore()
			if err != nil {
				return err
			}
			if err := creds.Delete(svc); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Disconnected %s (token removed from the keychain).\n", svc)
			return nil
		},
	}
}

func newMCPStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show integration connection state and which tools are exposed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openConfigStore()
			if err != nil {
				return err
			}
			defer func() { _ = s.Close() }()
			cfg, err := mcpserver.LoadConfig(ctx, s)
			if err != nil {
				return err
			}
			creds, err := mcpserver.NewCredStore()
			if err != nil {
				return err
			}
			for _, svc := range mcpserver.Services {
				tok, terr := creds.Token(svc)
				state := "not connected"
				if terr != nil {
					state = "keychain error: " + terr.Error()
				} else if tok != "" {
					state = "connected"
					if ident, verr := mcpserver.Verifier(svc)(ctx, tok); verr == nil {
						state = "connected as " + ident
					} else {
						state = "connected, but verification failed: " + verr.Error()
					}
				}
				flags := []string{}
				if !cfg.Enabled[svc] {
					flags = append(flags, "disabled")
				}
				if cfg.ReadOnly[svc] {
					flags = append(flags, "read-only")
				}
				suffix := ""
				if len(flags) > 0 {
					suffix = " [" + strings.Join(flags, ", ") + "]"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-8s %s%s\n", svc, state, suffix)
			}
			_, registered := mcpserver.BuildServer(cfg, creds)
			fmt.Fprintf(cmd.OutOrStdout(), "\nTools exposed (%d): %s\n", len(registered), strings.Join(registered, ", "))
			if len(cfg.Disabled) > 0 {
				names := make([]string, 0, len(cfg.Disabled))
				for n := range cfg.Disabled {
					names = append(names, n)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Disabled tools: %s\n", strings.Join(names, ", "))
			}
			return nil
		},
	}
}

func newMCPRegisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "register",
		Short: "Register this MCP server with Claude Code (runs `claude mcp add`)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			self, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locate aimonitor binary: %w", err)
			}
			claude, err := exec.LookPath("claude")
			if err != nil {
				return fmt.Errorf("`claude` CLI not found in PATH — install Claude Code first")
			}
			// User scope: available in every project, stored in ~/.claude.json.
			run := exec.CommandContext(cmd.Context(), claude, "mcp", "add", "--scope", "user", "aimonitor", "--", self, "mcp", "serve")
			run.Stdout = cmd.OutOrStdout()
			run.Stderr = cmd.ErrOrStderr()
			if err := run.Run(); err != nil {
				return fmt.Errorf("claude mcp add: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Registered. New Claude Code sessions will see the aimonitor tools.")
			return nil
		},
	}
}
