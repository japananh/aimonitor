package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/japananh/aimonitor/internal/config"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/secret"
	"github.com/japananh/aimonitor/internal/store"
	"github.com/spf13/cobra"
)

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run a battery of health checks (JSONL dir, keyring, DB, config)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd)
		},
	}
}

// doctorCheck is one assertion in the diagnostics run. ok=true means
// healthy; ok=false leaves a non-zero exit code without crashing the
// rest of the run.
type doctorCheck struct {
	name   string
	ok     bool
	detail string
}

func runDoctor(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()
	ctx := cmd.Context()
	var checks []doctorCheck

	// Config load.
	cfg, err := config.Load("")
	if err != nil {
		checks = append(checks, doctorCheck{"config load", false, err.Error()})
	} else {
		path, _ := config.DefaultPath()
		checks = append(checks, doctorCheck{"config load", true, path})
		_ = cfg // value-only sanity for now
	}

	// SQLite open.
	s, err := openStore()
	if err != nil {
		checks = append(checks, doctorCheck{"sqlite open", false, err.Error()})
		printChecks(out, checks)
		return errors.New("doctor: critical failure (sqlite)")
	}
	defer s.Close()
	checks = append(checks, doctorCheck{"sqlite open", true, s.Path})

	// Provider register.
	p, err := provider.Lookup("claude")
	if err != nil {
		checks = append(checks, doctorCheck{"claude provider", false, err.Error()})
	} else {
		checks = append(checks, doctorCheck{"claude provider", true, p.Name()})
	}

	// JSONL dir exists and is readable.
	checks = append(checks, checkJSONLDir())

	// Keyring sanity: round-trip a tiny temp entry.
	checks = append(checks, checkKeyring(ctx))

	// Accounts loaded.
	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		checks = append(checks, doctorCheck{"accounts", false, err.Error()})
	} else {
		checks = append(checks, doctorCheck{"accounts", true, fmt.Sprintf("%d configured", len(accounts))})
	}

	// Last usage snapshot per account. This is the live OAuth data the
	// daemon fetches from /api/oauth/usage (the oauth_usage table) — the
	// source that replaced the deprecated server-side probe. A missing row
	// just means the daemon has not fetched for this account yet; a present
	// row shows current utilization and how long ago it was fetched, so a
	// daemon that has stopped fetching shows up here as an ever-growing age.
	for _, a := range accounts {
		lim, err := s.GetLimits(ctx, a.ID)
		switch {
		case errors.Is(err, store.ErrLimitsNotFound):
			checks = append(checks, doctorCheck{"usage " + a.Label, true, "no usage yet"})
		case err != nil:
			checks = append(checks, doctorCheck{"usage " + a.Label, false, err.Error()})
		default:
			checks = append(checks, doctorCheck{"usage " + a.Label, true,
				fmt.Sprintf("5h=%.0f%% 7d=%.0f%%, fetched %s ago",
					lim.FiveHourPct, lim.SevenDayPct,
					time.Since(lim.FetchedAt).Truncate(time.Second))})
		}
	}

	printChecks(out, checks)
	for _, c := range checks {
		if !c.ok {
			return errors.New("doctor: one or more checks failed")
		}
	}
	return nil
}

func checkJSONLDir() doctorCheck {
	home, err := os.UserHomeDir()
	if err != nil {
		return doctorCheck{"jsonl dir", false, err.Error()}
	}
	dir := filepath.Join(home, ".claude", "projects")
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return doctorCheck{"jsonl dir", true, "not present yet (Claude Code not used here)"}
	}
	if err != nil {
		return doctorCheck{"jsonl dir", false, err.Error()}
	}
	if !info.IsDir() {
		return doctorCheck{"jsonl dir", false, dir + " is not a directory"}
	}
	return doctorCheck{"jsonl dir", true, dir}
}

func checkKeyring(ctx context.Context) doctorCheck {
	k, err := secret.Default()
	if err != nil {
		return doctorCheck{"keyring", false, err.Error()}
	}
	const (
		svc  = "aimonitor-doctor-probe"
		acct = "doctor"
		body = "ok"
	)
	if err := k.Set(svc, acct, []byte(body)); err != nil {
		return doctorCheck{"keyring", false, "Set: " + err.Error()}
	}
	defer func() { _ = k.Delete(svc, acct) }()

	got, err := k.Get(svc, acct)
	if err != nil {
		return doctorCheck{"keyring", false, "Get: " + err.Error()}
	}
	if string(got) != body {
		return doctorCheck{"keyring", false, "Get returned unexpected bytes"}
	}
	return doctorCheck{"keyring", true, "round-trip ok"}
}

func printChecks(w io.Writer, checks []doctorCheck) {
	for _, c := range checks {
		marker := "✓"
		if !c.ok {
			marker = "✗"
		}
		fmt.Fprintf(w, "  %s %s\t%s\n", marker, c.name, strings.TrimSpace(c.detail))
	}
}
