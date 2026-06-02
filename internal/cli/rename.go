package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/store"
)

func newRenameCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old-label> <new-label>",
		Short: "Rename an account",
		Long: `Rename an account's label. The keychain stash, stored credentials, and
identity are untouched — only the display label changes. The menu-bar
widget and the daemon's published status pick up the new name on their
next refresh.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			oldLabel := strings.TrimSpace(args[0])
			newLabel := strings.TrimSpace(args[1])
			if newLabel == "" {
				return errors.New("new label is required")
			}
			if oldLabel == newLabel {
				return nil // no-op
			}
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, _ provider.Provider) error {
				if err := s.RenameAccount(ctx, oldLabel, newLabel); err != nil {
					if errors.Is(err, store.ErrAccountNotFound) {
						return fmt.Errorf("no account labeled %q (see `aimonitor list`)", oldLabel)
					}
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Renamed %q → %q.\n", oldLabel, newLabel)
				return nil
			})
		},
	}
}
