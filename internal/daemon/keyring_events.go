package daemon

import (
	"fmt"
	"os"
	"time"

	"github.com/japananh/aimonitor/internal/secret"
)

// debugKeychainFailEnv, when set to a duration (e.g. "45s"), swaps the OS
// keychain for an empty in-memory one whose reads fail transiently for that
// long. It exists to exercise the degraded path in a dev daemon without
// touching the real keychain.
const debugKeychainFailEnv = "AIMONITOR_DEBUG_KEYCHAIN_FAIL_FOR"

// EnableKeychainResilience makes the daemon's keychain reads retry on a
// backoff and report a transient failure once. Call before the first
// keychain use.
func EnableKeychainResilience() error {
	var inner secret.Keyring
	if v := os.Getenv(debugKeychainFailEnv); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s: %w", debugKeychainFailEnv, err)
		}
		inner = secret.NewFaultKeyring(secret.NewMemoryKeyring(), d)
	}
	return secret.EnableResilientDefault(inner, logKeyringEvent)
}

func logKeyringEvent(e secret.Event) {
	switch e.Kind {
	case secret.EventDegraded:
		logger.Warn("keychain read failing; keeping last known account and retrying",
			"item", e.Service+"/"+e.Account, "err", e.Err)
	case secret.EventRecovered:
		logger.Info("keychain read recovered",
			"item", e.Service+"/"+e.Account, "after", e.Down.Round(time.Second), "failures", e.Failures)
	}
}
