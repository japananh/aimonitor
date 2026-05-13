# Thresholds and auto-switch

`aimonitor` decides when to auto-switch using a small, deterministic algorithm.

## Configuration

```sh
aimonitor config set thresholds 40,60,100
```

Valid configurations:

- At least one value.
- Every value is a positive integer in `(0, 100]`.
- Values are strictly ascending (no duplicates).

Default: `40,60,100`.

## How a tripwire fires

The daemon keeps an in-memory **local % used** for the active account, derived from the JSONL transcripts on this machine. Whenever a fresh usage sample bumps that value across one of the configured thresholds (e.g. crosses from 39% to 41% when 40 is in the list), a tripwire fires.

## What happens on a tripwire

1. Collect every other configured account whose local % used is strictly less than the just-crossed tripwire.
2. Cap the candidate set at K=3 by lowest local %.
3. For each surviving candidate, issue a one-shot **server-side rate-limit probe** (a tiny request to Anthropic that costs ~10 tokens). Parse `anthropic-ratelimit-tokens-remaining` and `anthropic-ratelimit-tokens-reset` from the response headers.
4. Pick the candidate with the highest probed `tokens_remaining` — but only if it is strictly higher than the current account's probed remaining.
5. Swap the OS-level credential blob, emit a desktop notification, write an audit row.

A 60-second cool-down between switches prevents thrashing.

## Why the probe is non-negotiable

The local JSONL estimate is blind to other devices using the same account. On a shared Claude Team seat, a teammate's morning session can drive the server-side counter to 95% while aimonitor on your Mac still says "0% locally." Without the probe, auto-switch would happily switch you onto an exhausted account. The probe is the only ground truth available outside the admin API.

See `_plans/kind-painting-noodle.md` §1 for the full rationale.
