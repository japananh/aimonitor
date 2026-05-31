package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// PostSwap is the best-effort cleanup that runs after a successful
// account swap. It currently does one thing: send SIGINT to running
// `claude` CLI processes that have been alive long enough to not be
// "just started by the user moments ago." Receiving SIGINT lets the
// CLI exit cleanly so the user's shell shows the prompt back; the
// next `claude` invocation re-reads the now-fresh credential.
//
// Future opt-in integrations (IDE reload via Accessibility, cmux/tmux
// pane restart) live in this same file when added — keep the
// process-mutation surface in one place so future audits don't have
// to chase callers through the codebase.
type PostSwap struct {
	// MinAge is the lower bound on a process's elapsed time before
	// SIGINT is sent. Protects against killing a process the user
	// just started in the new account (typical case: they ran
	// `claude` in terminal 1 and clicked Switch in the popover
	// simultaneously). Default 30 s.
	MinAge time.Duration

	// LogPath is where post-swap events are appended for audit. Empty
	// means $HOME/.aimonitor/post-swap.log. Failure to open the log
	// is non-fatal — the SIGINT still happens.
	LogPath string

	// Now is the clock. Tests override; default time.Now.
	Now func() time.Time

	// EnumPIDs returns the PIDs of currently-running `claude`
	// processes. Tests inject a deterministic enumerator; the default
	// shells out to pgrep.
	EnumPIDs func(ctx context.Context) ([]int, error)

	// ElapsedFor returns how long pid has been running. Tests inject;
	// the default shells out to `ps`.
	ElapsedFor func(ctx context.Context, pid int) (time.Duration, error)

	// Signal is the signal sent to qualifying processes. Default
	// syscall.SIGINT. Overridable so tests can use a no-op.
	Signal syscall.Signal
}

func (p *PostSwap) defaults() {
	if p.MinAge == 0 {
		p.MinAge = 30 * time.Second
	}
	if p.Now == nil {
		p.Now = time.Now
	}
	if p.EnumPIDs == nil {
		p.EnumPIDs = enumerateClaudePIDs
	}
	if p.ElapsedFor == nil {
		p.ElapsedFor = processElapsed
	}
	if p.Signal == 0 {
		p.Signal = syscall.SIGINT
	}
}

// Run is the entry point a Switcher invokes via PostSwapHook. fromLabel
// and toLabel are recorded in the log; the function tolerates either
// being empty (e.g. on first swap with no previous active account).
func (p *PostSwap) Run(ctx context.Context, fromLabel, toLabel string) {
	p.defaults()

	log := p.openLog()
	defer func() {
		if c, ok := log.(io.Closer); ok {
			_ = c.Close()
		}
	}()

	pids, err := p.EnumPIDs(ctx)
	if err != nil {
		fmt.Fprintf(log, "[%s] swap %q->%q: enumerate failed: %v\n",
			p.Now().Format(time.RFC3339), fromLabel, toLabel, err)
		return
	}

	for _, pid := range pids {
		age, err := p.ElapsedFor(ctx, pid)
		if err != nil {
			// Process may have exited between enumeration and ps —
			// race is common, not worth logging.
			continue
		}
		if age < p.MinAge {
			fmt.Fprintf(log, "[%s] swap %q->%q: skip pid %d (age %s < min %s)\n",
				p.Now().Format(time.RFC3339), fromLabel, toLabel, pid, age, p.MinAge)
			continue
		}
		if err := syscall.Kill(pid, p.Signal); err != nil {
			fmt.Fprintf(log, "[%s] swap %q->%q: kill pid %d: %v\n",
				p.Now().Format(time.RFC3339), fromLabel, toLabel, pid, err)
			continue
		}
		fmt.Fprintf(log, "[%s] swap %q->%q: sent %s to pid %d (age %s)\n",
			p.Now().Format(time.RFC3339), fromLabel, toLabel, p.Signal, pid, age)
	}
}

// openLog returns a write-only handle to the post-swap log. Falls back
// to os.Stderr on any failure so events still surface; the caller
// shouldn't have to error-check.
func (p *PostSwap) openLog() io.Writer {
	path := p.LogPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return os.Stderr
		}
		dir := home + "/.aimonitor"
		_ = os.MkdirAll(dir, 0o700)
		path = dir + "/post-swap.log"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return os.Stderr
	}
	return f
}

// enumerateClaudePIDs shells out to pgrep with -x to match the
// command name exactly (avoids catching tools like claude-monitor or
// aimonitor itself, whose argv may contain "claude" elsewhere).
func enumerateClaudePIDs(ctx context.Context) ([]int, error) {
	cmd := exec.CommandContext(ctx, "pgrep", "-x", "claude")
	out, err := cmd.Output()
	if err != nil {
		// pgrep exits 1 when no processes match — not a real error.
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("pgrep: %w", err)
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			continue
		}
		pids = append(pids, n)
	}
	return pids, nil
}

// processElapsed asks `ps` how long pid has been running. ps's etime
// column emits HH:MM:SS or DD-HH:MM:SS or MM:SS. We parse all three.
func processElapsed(ctx context.Context, pid int) (time.Duration, error) {
	cmd := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "etime=")
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ps: %w", err)
	}
	return parseEtime(strings.TrimSpace(string(out)))
}

// parseEtime understands ps's elapsed-time formats:
//
//	MM:SS              (< 1 h)
//	HH:MM:SS           (< 1 d)
//	DD-HH:MM:SS        (>= 1 d)
//
// Returns 0 + error on any parse failure. Exposed for tests.
func parseEtime(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty etime")
	}
	var days int
	rest := s
	if i := strings.Index(s, "-"); i > 0 {
		d, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("parse days %q: %w", s[:i], err)
		}
		days = d
		rest = s[i+1:]
	}
	parts := strings.Split(rest, ":")
	var h, m, sec int
	var err error
	switch len(parts) {
	case 2: // MM:SS
		m, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		sec, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, err
		}
	case 3: // HH:MM:SS
		h, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, err
		}
		m, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, err
		}
		sec, err = strconv.Atoi(parts[2])
		if err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unexpected etime shape: %q", s)
	}
	d := time.Duration(days)*24*time.Hour +
		time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(sec)*time.Second
	return d, nil
}
