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
automatically).

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
  are re-fetched before it can be judged).
- 10-minute cooldown after a "no candidate" decision.
- A manual switch (CLI or widget) always wins; auto-switch re-evaluates
  from the new active account.
