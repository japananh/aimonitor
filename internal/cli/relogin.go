package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/japananh/aimonitor/internal/claudeconfig"
	"github.com/japananh/aimonitor/internal/mcpserver"
	"github.com/japananh/aimonitor/internal/provider"
	"github.com/japananh/aimonitor/internal/provider/claude"
	"github.com/japananh/aimonitor/internal/store"
)

// Config keys for the (optional) magic-link auto-fetch. The link *source*
// is pluggable — Slack is the first one, but the login link might just as
// well arrive by email, Telegram, Discord, etc. relogin.link_source names
// the source; each source reads its own relogin.<source>.* settings.
const (
	settingsKeyLinkSource   = "relogin.link_source"   // e.g. "slack"; unset ⇒ manual
	settingsKeySlackChannel = "relogin.slack.channel" // channel ID for the "slack" source
)

type reloginOpts struct {
	all     bool
	channel string // override for the slack source's channel
	manual  bool   // skip auto-fetch; open the link yourself
	timeout time.Duration
}

func newReloginCmd() *cobra.Command {
	var opts reloginOpts
	cmd := &cobra.Command{
		Use:   "relogin [label ...]",
		Short: "Re-authenticate accounts whose Claude session expired",
		Long: `Re-login one or more accounts after their OAuth session expires.

For each account it:
  1. Opens claude.ai and copies the account email to your clipboard, then
     waits while you sign in — however your accounts log in (password,
     SSO, email magic-link, …).
  2. Captures the fresh Claude Code credential once you run 'claude' +
     '/login' (it auto-approves, since you're already signed in), writes
     it into this account's slot, and clears the "session expired" flag —
     refreshing the live slot too if the account was active.

Optional auto-fetch: if your login links are delivered somewhere machine-
readable, aimonitor can grab the right one and open it for you, matched by
the account's email. The source is pluggable via 'relogin.link_source';
Slack is supported today (set 'relogin.link_source slack' and
'relogin.slack.channel <id>', with Slack connected). Skip it with --manual.

With no arguments, re-logs every account currently flagged as needing
re-login. Pass labels to target specific accounts, or --all for every
registered account.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withRuntime(cmd.Context(), func(ctx context.Context, s *store.Store, p provider.Provider) error {
				return runRelogin(ctx, cmd, s, p, args, opts)
			})
		},
	}
	cmd.Flags().BoolVar(&opts.all, "all", false, "re-login every registered account")
	cmd.Flags().StringVar(&opts.channel, "channel", "", "override the slack source's channel ID for this run")
	cmd.Flags().BoolVar(&opts.manual, "manual", false, "don't auto-fetch the login link; open it yourself")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 3*time.Minute, "how long to wait for the login link and the new credential")
	return cmd
}

func runRelogin(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, args []string, opts reloginOpts) error {
	out := cmd.OutOrStdout()

	targets, err := reloginTargets(ctx, s, args, opts.all)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Fprintln(out, "No accounts need re-login. Pass a label (e.g. `aimonitor relogin \"BE 2\"`) or --all.")
		return nil
	}

	// Resolve the (optional) auto-fetch source once. A nil source means
	// manual mode; note explains why so the user isn't left guessing.
	src, note := resolveLinkSource(ctx, s, opts)
	if note != "" {
		fmt.Fprintln(out, note)
	}

	var failed []string
	for _, a := range targets {
		if err := reloginOne(ctx, cmd, s, p, a, src, opts.timeout); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "✗ %s: %v\n", a.Label, err)
			failed = append(failed, a.Label)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("re-login failed for: %s", strings.Join(failed, ", "))
	}
	return nil
}

// reloginTargets resolves which accounts to re-login: explicit labels,
// every account (--all), or (default) only those flagged NeedsRelogin.
func reloginTargets(ctx context.Context, s *store.Store, labels []string, all bool) ([]store.Account, error) {
	if len(labels) > 0 {
		out := make([]store.Account, 0, len(labels))
		for _, l := range labels {
			a, err := s.GetAccountByLabel(ctx, l)
			if err != nil {
				return nil, fmt.Errorf("account %q: %w", l, err)
			}
			out = append(out, a)
		}
		return out, nil
	}
	accts, err := s.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	if all {
		return accts, nil
	}
	var flagged []store.Account
	for _, a := range accts {
		if a.NeedsRelogin {
			flagged = append(flagged, a)
		}
	}
	return flagged, nil
}

func reloginOne(ctx context.Context, cmd *cobra.Command, s *store.Store, p provider.Provider, a store.Account, src magicLinkSource, timeout time.Duration) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "\n── Re-login: %s (%s) ──\n", a.Label, dash(a.Email))

	if a.Email == "" {
		return fmt.Errorf("account has no recorded email — can't match its login link; `aimonitor add` it again with --email")
	}

	// Is this the currently-active account? If so we refresh the live slot
	// after capture (CaptureNew restores the *pre-login* bytes, which would
	// leave the active account on its stale token otherwise).
	wasActive := false
	if preLive, err := p.ActiveCredential(ctx); err == nil {
		wasActive = stashMatches(ctx, a.KeyringRef, preLive.Bytes)
	}

	// Step 1 — start the login in the browser.
	openURL("https://claude.ai/login")
	note := ""
	if copyClipboard(a.Email) {
		note = " (copied to clipboard)"
	}
	fmt.Fprintf(out, "1. Opened claude.ai — sign in with:  %s%s\n", a.Email, note)

	// Step 2 — auto-open the matching login link, if a source is configured.
	// Bound the search at "now" so we never reuse an already-consumed link.
	if src != nil {
		since := time.Now()
		fmt.Fprintf(out, "   Watching %s for your login link…\n", src.label())
		link, err := pollMagicLink(ctx, src, a.Email, since, timeout)
		switch {
		case err != nil:
			fmt.Fprintf(cmd.ErrOrStderr(), "   (couldn't fetch the link: %v — open it yourself)\n", err)
		case link != "":
			openURL(link)
			fmt.Fprintln(out, "   ✓ Login link arrived — opened it. Wait for claude.ai to show success.")
		default:
			fmt.Fprintln(out, "   (no login link seen in time — open it yourself)")
		}
	} else {
		fmt.Fprintln(out, "   Finish signing in in the browser, then wait for the success page.")
	}

	// Step 3 — capture the fresh Claude Code credential.
	fmt.Fprintln(out, "2. Now run `claude`, type `/login`, and finish in the browser (it auto-approves).")
	cred, err := claude.CaptureNew(ctx, out, claude.CaptureOpts{NewLabel: a.Label, Timeout: timeout})
	if err != nil {
		return err
	}
	defer cred.Zero()

	// Safety: refuse to overwrite this slot with a different account's login.
	cc, ccErr := claudeconfig.New()
	ident := resolveIdentity(ctx, cmd, cc, ccErr, a.Email)
	if ident.Email != "" && !strings.EqualFold(ident.Email, a.Email) {
		return fmt.Errorf("you signed into %s but this slot is %s — not overwriting; re-run and log into the right account", ident.Email, a.Email)
	}

	if err := claude.StashCredential(ctx, a.KeyringRef, cred); err != nil {
		return fmt.Errorf("save refreshed credential: %w", err)
	}
	if ident.Email != "" {
		_ = s.UpdateAccountIdentity(ctx, a.ID, ident.Email, ident.OrganizationUUID, ident.OrganizationName)
	}
	_ = s.SetNeedsRelogin(ctx, a.ID, false)

	if wasActive {
		if err := p.SetActiveCredential(ctx, cred); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "   warning: refreshed the stash but couldn't update the active slot: %v (run `aimonitor switch %q`)\n", err, a.Label)
		}
	}

	fmt.Fprintf(out, "✓ %s refreshed — session-expired flag cleared.\n", a.Label)
	return nil
}

// magicLinkSource fetches a claude.ai magic-link for an account. Slack is
// the first implementation; email / Telegram / Discord / a local IMAP box
// / a webhook can each drop in as another implementation without the
// relogin flow changing — it only depends on this interface.
type magicLinkSource interface {
	// label is a short human name shown in progress output.
	label() string
	// fetch returns the freshest login link for email generated at or
	// after since, or "" (no error) when none is available yet.
	fetch(ctx context.Context, email string, since time.Time) (string, error)
}

// resolveLinkSource picks the configured auto-fetch source, or (nil, note)
// for manual mode — note explains the fallback so the user isn't guessing.
func resolveLinkSource(ctx context.Context, s *store.Store, opts reloginOpts) (magicLinkSource, string) {
	if opts.manual {
		return nil, ""
	}
	kind, _ := s.GetSetting(ctx, settingsKeyLinkSource)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return nil, "(Tip: set `relogin.link_source slack` + `relogin.slack.channel <id>` to auto-open login links. Opening manually for now.)"
	case "slack":
		channel := opts.channel
		if channel == "" {
			channel, _ = s.GetSetting(ctx, settingsKeySlackChannel)
		}
		if channel == "" {
			return nil, "(Set `relogin.slack.channel <id>` to auto-open links from Slack. Opening manually for now.)"
		}
		c := connectedSlackClient()
		if c == nil {
			return nil, "(Slack isn't connected — `aimonitor mcp connect slack`. Opening manually for now.)"
		}
		return slackLinkSource{c: c, channel: channel}, ""
	default:
		return nil, fmt.Sprintf("(relogin.link_source %q isn't supported yet — only 'slack' so far. Opening manually.)", kind)
	}
}

// pollMagicLink asks src for the link until one appears, timeout elapses,
// or ctx is cancelled. Returns ("", nil) on timeout so the caller can fall
// back to manual.
func pollMagicLink(ctx context.Context, src magicLinkSource, email string, since time.Time, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		link, err := src.fetch(ctx, email, since)
		if err != nil {
			return "", err
		}
		if link != "" {
			return link, nil
		}
		if time.Now().After(deadline) {
			return "", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// slackLinkSource reads login links from a Slack channel via the same
// keyring-backed client the MCP server uses.
type slackLinkSource struct {
	c       *mcpserver.Client
	channel string
}

func (s slackLinkSource) label() string { return "Slack channel " + s.channel }

func (s slackLinkSource) fetch(ctx context.Context, email string, since time.Time) (string, error) {
	// Slack's history cursor is a unix-seconds ts string.
	return s.c.LatestMagicLink(ctx, s.channel, email, fmt.Sprintf("%d.000000", since.Unix()))
}

// connectedSlackClient returns an MCP Slack client only when Slack is
// connected; nil otherwise so callers fall back to manual link-opening.
func connectedSlackClient() *mcpserver.Client {
	creds, err := mcpserver.NewCredStore()
	if err != nil {
		return nil
	}
	if tok, _ := creds.Token(mcpserver.ServiceSlack); strings.TrimSpace(tok) == "" {
		return nil
	}
	return mcpserver.NewClient(creds)
}

// openURL opens u in the user's default browser, best-effort. A failure
// (headless box, missing opener) is silent — the URL is always printed too.
func openURL(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "linux":
		c = exec.Command("xdg-open", u)
	default:
		return
	}
	_ = c.Start()
}

// copyClipboard puts s on the clipboard, best-effort. Returns whether it
// succeeded so the caller can hint "(copied to clipboard)".
func copyClipboard(s string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	c := exec.Command("pbcopy")
	c.Stdin = strings.NewReader(s)
	return c.Run() == nil
}
