// Package provider defines the abstraction every AI-provider integration
// must implement. In v1 the only implementation is the Claude provider; v2
// will add an OpenAI Codex / GitHub Copilot CLI provider.
//
// The interface is intentionally narrow. Concrete providers handle:
//   - listing the accounts that are stashed in the OS keyring under this
//     provider's namespace,
//   - producing a local-only usage estimate from on-disk transcripts,
//   - issuing a server-side probe that reveals the true remaining rate-limit
//     allowance for the account (the ground truth the auto-switcher gates on),
//   - reading and writing the "currently active" credential blob in the
//     OS-level slot that the underlying CLI tool reads from,
//   - guiding a user through the onboarding flow that captures a new account.
package provider

import (
	"context"
	"time"
)

// Provider is the contract every AI-provider integration must satisfy.
type Provider interface {
	// Name returns the stable identifier used in storage, config, and the
	// registry (e.g. "claude").
	Name() string

	// LoadAccounts returns every account stashed in the OS keyring under
	// this provider's namespace, in arbitrary order.
	LoadAccounts(ctx context.Context) ([]Account, error)

	// EstimateSessionUsage produces a local-only estimate of how much of the
	// current session window the account has consumed on THIS machine. It
	// cannot see other devices on the same account.
	EstimateSessionUsage(ctx context.Context, acct Account) (Usage, error)

	// ProbeServerSide issues one tiny request to the provider's API and
	// reads the rate-limit response headers. This is the ground truth the
	// auto-switcher uses to gate decisions. It must NEVER alter the
	// currently-active credential slot.
	ProbeServerSide(ctx context.Context, acct Account) (RateLimit, error)

	// ActiveCredential reads the credential blob currently installed in the
	// OS-level slot that the underlying CLI tool reads from (for Claude:
	// the macOS Keychain entry "Claude Code-credentials").
	ActiveCredential(ctx context.Context) (Credential, error)

	// SetActiveCredential writes a credential blob into that slot. Used by
	// `aimonitor switch` and by the auto-switch engine.
	SetActiveCredential(ctx context.Context, cred Credential) error

	// OnboardingFlow walks the user through capturing a brand-new
	// credential. For Claude this stashes the active blob, invokes
	// `claude login`, reads back the freshly-written blob, then restores
	// the stash so the previously-active account stays active. The
	// returned Credential is the new account's blob.
	OnboardingFlow(ctx context.Context) (Credential, error)
}

// Account is a configured aimonitor account, identifying which keyring
// entry holds its OAuth blob.
type Account struct {
	ID         int64
	Provider   string
	Label      string
	Email      string
	KeyringRef string // e.g. "aimonitor-acct-<uuid>" in macOS Keychain
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// Credential is the opaque OAuth blob stored in the OS keyring. The plain
// bytes are sensitive — callers must zero them as soon as possible.
type Credential struct {
	// Bytes is the raw JSON blob (or whatever serialization the provider
	// uses). Treat as secret material.
	Bytes []byte
}

// Zero overwrites Bytes with zeros to reduce the window in which token
// material lives in process memory.
func (c *Credential) Zero() {
	for i := range c.Bytes {
		c.Bytes[i] = 0
	}
	c.Bytes = nil
}

// Usage is a local, per-machine estimate of an account's consumption inside
// the current session window. UnknownBudget=true means we don't know the
// total quota yet, so PercentUsed is a best-effort observed-maximum estimate.
type Usage struct {
	AccountID         int64
	WindowStart       time.Time
	WindowEnd         time.Time
	InputTokens       int64
	OutputTokens      int64
	CacheReadTokens   int64
	CacheWriteTokens  int64
	PercentUsed       float64 // 0..100, may be heuristic
	UnknownBudget     bool
	SampledAt         time.Time
}

// RateLimit is the server-side truth obtained via a one-shot probe.
type RateLimit struct {
	AccountID       int64
	ProbedAt        time.Time
	TokensRemaining int64
	ResetAt         time.Time
	HTTPStatus      int
}
