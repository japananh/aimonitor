package claude

import "errors"

// CredentialUnusableError means an account's stashed credential cannot
// authenticate at all and no token refresh can recover it — e.g. Claude Code
// blanked the tokens on logout/expiry, leaving a `claudeAiOauth` blob with empty
// accessToken and refreshToken. The only fix is a fresh login, so the daemon
// treats it exactly like a dead refresh token: flag needs_relogin.
//
// This is distinct from a transient failure (network, 429): those leave the
// flag untouched because retrying can succeed.
type CredentialUnusableError struct {
	Label  string
	Reason string
}

func (e *CredentialUnusableError) Error() string {
	return "credential unusable for " + e.Label + ": " + e.Reason
}

// IsCredentialUnusable reports whether err (or anything it wraps) is a
// *CredentialUnusableError.
func IsCredentialUnusable(err error) bool {
	var e *CredentialUnusableError
	return errors.As(err, &e)
}

// RequiresRelogin reports whether err means an account can only be recovered by
// a fresh login. Three disjoint conditions converge here:
//   - a dead refresh token (refresh endpoint 400/401 → TokenRefreshExpiredError),
//   - a server-rejected access token (usage-endpoint 401 → UsageAuthError), and
//   - a stash with no usable tokens (CredentialUnusableError).
//
// Callers use it to decide when to set needs_relogin; every other error
// (network, 429, generic HTTP) must leave the flag untouched, since retrying or
// a later refresh can clear it.
func RequiresRelogin(err error) bool {
	return IsRefreshExpired(err) || IsAuthError(err) || IsCredentialUnusable(err)
}
