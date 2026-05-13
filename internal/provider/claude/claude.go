// Package claude implements the Provider interface for Anthropic's Claude
// Code OAuth ecosystem. In v1 this is the only provider; v2 will add
// alongside (not replace) an OpenAI/Codex implementation.
//
// Behavioural surface as of Phase 2:
//   - ActiveCredential / SetActiveCredential are wired to the OS keyring.
//   - OnboardingFlow runs the defensive byte-diff dance around
//     `claude login` (see onboarding.go).
//   - LoadAccounts, EstimateSessionUsage, ProbeServerSide remain stubs
//     until later in Phase 2 (LoadAccounts depends on the daemon's view
//     of the accounts table) and Phase 3 (Probe).
package claude

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/japananh/aimonitor/internal/provider"
)

// Name is the stable provider identifier.
const Name = "claude"

// ErrNotImplemented is the placeholder error still returned by the
// behaviours that haven't been wired yet.
var ErrNotImplemented = errors.New("claude provider: not implemented in v1.0.0-beta")

// Provider is the Claude implementation of provider.Provider.
//
// The keychainOps backend is constructed lazily on first use so that
// merely importing the package (e.g. for init-time registration) doesn't
// touch the OS keyring or fail when libsecret/Keychain isn't available
// in a constrained context like a test or a `--help` invocation.
type Provider struct {
	keysOnce sync.Once
	keys     *keychainOps
	keysErr  error
}

// New returns a fresh Claude provider instance.
func New() *Provider { return &Provider{} }

// Name implements provider.Provider.
func (p *Provider) Name() string { return Name }

func (p *Provider) ops() (*keychainOps, error) {
	p.keysOnce.Do(func() {
		p.keys, p.keysErr = newKeychainOps()
	})
	return p.keys, p.keysErr
}

// LoadAccounts implements provider.Provider.
//
// Stub: Phase 2 daemon layer constructs Account values from SQLite. The
// Provider's responsibility here is mostly to declare its provider name
// and let the storage layer enumerate. We'll revisit if the API turns
// out to need more.
func (p *Provider) LoadAccounts(_ context.Context) ([]provider.Account, error) {
	return nil, ErrNotImplemented
}

// EstimateSessionUsage implements provider.Provider.
//
// Stub: the JSONL parser + watcher exist in this package and in
// internal/daemon, but plumbing them into a per-account session-window
// number lives in the daemon. We'll fill this in when the daemon's
// usage aggregator lands.
func (p *Provider) EstimateSessionUsage(_ context.Context, _ provider.Account) (provider.Usage, error) {
	return provider.Usage{}, ErrNotImplemented
}

// ProbeServerSide implements provider.Provider.
//
// It reads the account's stash, asks the production Prober to issue a
// 1-token request against Anthropic's API, and returns the parsed
// rate-limit-header truth. Callers that want caching should use the
// store's probe_results table around this call.
func (p *Provider) ProbeServerSide(ctx context.Context, acct provider.Account) (provider.RateLimit, error) {
	cred, err := RetrieveStash(ctx, acct.KeyringRef)
	if err != nil {
		return provider.RateLimit{}, fmt.Errorf("probe: read stash for %q: %w", acct.Label, err)
	}
	defer cred.Zero()

	rl, err := NewProber().Probe(ctx, cred)
	rl.AccountID = acct.ID
	return rl, err
}

// ActiveCredential implements provider.Provider.
func (p *Provider) ActiveCredential(ctx context.Context) (provider.Credential, error) {
	k, err := p.ops()
	if err != nil {
		return provider.Credential{}, err
	}
	return k.readActive(ctx)
}

// SetActiveCredential implements provider.Provider.
func (p *Provider) SetActiveCredential(ctx context.Context, cred provider.Credential) error {
	k, err := p.ops()
	if err != nil {
		return err
	}
	return k.writeActive(ctx, cred)
}

// OnboardingFlow implements provider.Provider.
func (p *Provider) OnboardingFlow(ctx context.Context) (provider.Credential, error) {
	k, err := p.ops()
	if err != nil {
		return provider.Credential{}, err
	}
	return runOnboarding(ctx, onboardingDeps{
		keys:  k,
		login: runClaudeLogin,
	})
}

func init() {
	provider.Register(New())
}
