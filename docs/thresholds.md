# Thresholds and auto-switch

aimonitor auto-switches using a small, deterministic algorithm driven by
Anthropic's own usage numbers (`/api/oauth/usage` — server-side truth,
consumes no tokens).

## Configuration

```sh
aimonitor config set auto_swap.enabled true          # master toggle (default true)
aimonitor config set auto_swap.threshold_pct 80      # 5-hour window threshold
aimonitor config set auto_swap.threshold_7d_pct 80   # 7-day window threshold
aimonitor config set auto_swap.grace_sec 60          # warning → switch delay; 0 = immediate
```

Thresholds accept any integer in `(0, 100]`. Both windows are checked
independently — crossing **either** one arms a switch.

## When a switch arms

The daemon polls the active account's usage every ~5 minutes (± jitter).
When the active account's 5-hour **or** 7-day utilization reaches its
threshold, a switch arms: a desktop notification announces the target and
the swap fires after `grace_sec` (time to wrap up a thought — running
`claude` sessions are never interrupted; they adopt the new credential
automatically). If the active account is already **exhausted** (100 % on
the binding window), the swap fires immediately — no grace delay — to
rescue sessions that can't make a request.

An armed switch cancels only when the active account drops back below the
threshold on **both** windows — a 5-hour reset doesn't clear a weekly cap.

## How the target is chosen

The window that crossed its threshold (the further over, when both) is the
**binding window**. Candidates are judged relative to the active account:

1. **Never** an account at ≥ 100 % on either window — it can't serve
   requests, and switching into it just ping-pongs back.
2. Prefer accounts lower than the active one on **both** windows, ranked by
   most overall headroom (lowest `max(5h, 7d)`).
3. Otherwise accept an account lower on the **binding** window only —
   escaping a weekly-capped account into a 5-hour-warm one is still a win,
   since 5-hour windows recover in hours while weekly caps last days.
4. Accounts whose usage data is stale or unknown are last-resort (the
   daemon refreshes stale candidates just-in-time before deciding, so this
   rarely applies). Ties break least-recently-used so accounts rotate.

If nothing beats the active account on the binding window, aimonitor stays
put and notifies that no account has more headroom.

## Anti-thrash guards

- 5-minute cooldown after every auto-switch (the fresh account's numbers
  are re-fetched before it can be judged). An active account that hits
  100 % bypasses this cooldown — a rescue can't wait out the window.
- 10-minute cooldown after a "no candidate" decision.
- A manual switch (CLI or widget) always wins; auto-switch re-evaluates
  from the new active account.

## Polling when there is nowhere to switch

- With no swap target, the active account is polled every 5 minutes, never
  at the 60-second near-limit cadence.
- An account at 100 % on a window is not polled again until that window
  resets. Auto-swap still re-checks the other accounts every 5 minutes.
- A 429 parks the account: for the server's `Retry-After` if it sends one,
  otherwise 15 min, then 30 min, then 1 h for each later 429. One success in
  between does not reset that growth. It resets after 2 h without a 429.
- These rules cover every path that fetches usage: the daemon, auto-swap's
  candidate check, and `aimonitor usage refresh` (the popover and the
  per-row Refresh button). A refresh within 5 minutes of the last fetch
  shows the stored numbers instead of calling Anthropic.
