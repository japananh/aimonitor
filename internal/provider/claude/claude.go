// Package claude implements the Provider interface for Anthropic's Claude
// Code OAuth ecosystem. In v1 this is the only provider; v2 will add
// alongside (not replace) an OpenAI/Codex implementation.
//
// All exported entry points are stubs that return ErrNotImplemented in this
// scaffolding milestone. Real implementations land in Phase 2 (JSONL parser,
// Keychain bridge, onboarding flow) and Phase 3 (rate-limit probe).
package claude

import (
	"context"
	"errors"

	"github.com/japananh/aimonitor/internal/provider"
)

// Name is the stable provider identifier.
const Name = "claude"

// ErrNotImplemented is the placeholder error returned by every method until
// the corresponding phase lands.
var ErrNotImplemented = errors.New("claude provider: not implemented in v1.0.0-beta scaffolding")

// Provider is the Claude implementation of provider.Provider.
type Provider struct{}

// New returns a fresh Claude provider instance.
func New() *Provider { return &Provider{} }

// Name implements provider.Provider.
func (p *Provider) Name() string { return Name }

// LoadAccounts implements provider.Provider.
func (p *Provider) LoadAccounts(ctx context.Context) ([]provider.Account, error) {
	return nil, ErrNotImplemented
}

// EstimateSessionUsage implements provider.Provider.
func (p *Provider) EstimateSessionUsage(ctx context.Context, acct provider.Account) (provider.Usage, error) {
	return provider.Usage{}, ErrNotImplemented
}

// ProbeServerSide implements provider.Provider.
func (p *Provider) ProbeServerSide(ctx context.Context, acct provider.Account) (provider.RateLimit, error) {
	return provider.RateLimit{}, ErrNotImplemented
}

// ActiveCredential implements provider.Provider.
func (p *Provider) ActiveCredential(ctx context.Context) (provider.Credential, error) {
	return provider.Credential{}, ErrNotImplemented
}

// SetActiveCredential implements provider.Provider.
func (p *Provider) SetActiveCredential(ctx context.Context, cred provider.Credential) error {
	return ErrNotImplemented
}

// OnboardingFlow implements provider.Provider.
func (p *Provider) OnboardingFlow(ctx context.Context) (provider.Credential, error) {
	return provider.Credential{}, ErrNotImplemented
}

func init() {
	provider.Register(New())
}
