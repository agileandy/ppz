# Plan: Message Priority for ppz (1=high, 2=medium, 3=low)

## Context

Today all ppz messages are equal — recipients see them strictly in arrival (JetStream
stream-sequence) order. Andy wants a simple priority on a message (`1=high, 2=medium,
3=low`) so a recipient reads/acts on higher-priority messages first. The change is
end-to-end: a `--priority` flag on `ppz send`, a `priority` field on the wire envelope,
and priority-first ordering when the daemon delivers the retained batch to `ppz read`.

Architecture recap (verified): CLI ↔ daemon over a Unix socket (`cliproto`), daemon
publishes `envelope.Message` JSON to embedded NATS JetStream, and `handleRead` in the
daemon drains retained messages into a slice before streaming them to the CLI — that
slice is the single place ordering is decided.

## Design decisions

1. **Unset = 0 on the wire; readers treat anything outside 1–3 as medium (2).**
   No default stamped at send time — legacy messages, unset sends, auto-acks, and
   batch (terminal-share) envelopes are all identical (`"priority": 0`). One shared
   helper `cliproto.EffectivePriority(p int) int` (returns 2 unless p∈[1,3]) is the
   only place this rule lives. CLI validates `--priority` before IPC; `handleSend`
   rejects values outside {0,1,2,3} at the trust boundary (new `EInvalidPriority`
   error code, same pattern as the existing `EInvalidSubject` check).
2. **Sort AFTER the Skip/Limit trim** in `handleRead` — `-l N`/`--skip` keep their
   documented arrival-window semantics; priority governs presentation order within
   the delivered window. Cursor advance uses `lastSeqSeen` from stream sequence
   (computed in the drain loop), so reordering the slice is pure presentation —
   cursors and auto-acks are untouched.
3. **Priority-ordered by default, `sort.SliceStable`, GATED** — FIFO preserved
   within a tier; a mesh where nobody sets a priority produces byte-identical output
   to today. The sort runs **only when `cliproto.IsTabularReadPipe(req.Channel)`
   (inbox/broadcast) AND `!req.Follow`**. Rationale (red-team findings 1 & 2):
   `handleRead` serves ALL pipe types, and WIRE.md §8 promises byte-faithful,
   arrival-ordered delivery for stdout/stdin/stdctrl/custom pipes — an ungated sort
   would silently reorder those streams if any sender set a priority on them. A
   `--priority` send to a non-inbox pipe is accepted but inert (field carried, never
   used for ordering) — documented.
4. **`--tail` (live follow) is fully arrival-ordered — backlog AND live.** Live
   messages stream one-at-a-time from an OrderedConsumer and can't be reordered;
   the drained backlog is deliberately left unsorted too (the `!req.Follow` gate in
   #3) so a single invocation never switches ordering discipline mid-stream.
   `reread` shares `handleRead` and is not Follow, so it sorts automatically — no
   separate work.
5. **CLI flag accepts numbers and names:** `--priority 1|2|3|high|medium|med|low`;
   `""` → 0 (unset); anything else → usage error, exit 2.
6. **Tabular display: badge only when explicitly non-default** — prepend `P1 ` /
   `P3 ` to the body for priorities 1 and 3; 0 and 2 render exactly as today (keeps
   every existing golden `expected.txt` byte-identical). `--json` carries the field
   for free.

## Changes (minimal, no adjacent refactors)

Priority travels through three structs: `SendRequest` (CLI→daemon) →
`envelope.Message` (NATS wire) → `ReadMessage` (daemon→CLI).

| File | Change |
|---|---|
| `internal/envelope/envelope.go:39-47` | Add `Priority int \`json:"priority"\`` to `Message` (**no omitempty** — WIRE.md §3 says all fields always serialized). Doc comment: 1=high 2=medium 3=low, 0=unset≡medium. |
| `internal/cliproto/types.go` | `SendRequest` (~:297): `Priority int \`json:"priority,omitempty"\``. `ReadMessage` (~:147): `Priority int \`json:"priority"\``. |
| `internal/cliproto/` (read_format.go or new file) | `EffectivePriority(p int) int` helper. Register `EInvalidPriority` in the error table. |
| `internal/cli/send.go:59-63` | `--priority` string flag + `parsePriority` helper (numbers + named aliases); pass into the `SendRequest` literal (~:108). |
| `internal/cli/send.go:191-196` | **Add `-priority`/`--priority` to the `valueFlags` map in `splitSendArgs`** — otherwise `ppz send --priority 1 bob hi` eats "1" as the target. Known foot-gun, covered by a test. |
| `internal/daemon/publish.go:120-125` | `buildBroadcastEnvelope`: `env.Priority = req.Priority`. |
| `internal/daemon/handlers.go:~906` | `handleSend`: reject invalid priority → `EInvalidPriority`. **Extract the check as a pure func** `validatePriority(p int) bool` (or fold into a `validateSendRequest`) so it's daemon-unit-testable — the CLI guard otherwise masks it in every expressible test (red-team finding 4). |
| `internal/daemon/read.go:256-264` and `:349-357` | Copy `env.Priority` into both `ReadMessage` construction sites (drain + follow). |
| `internal/daemon/read.go` (after :284, before :286) | New `sortRetainedByPriority(retained)` — `sort.SliceStable` on `EffectivePriority` — called between the Limit trim and the encode loop, **gated: `if !req.Follow && cliproto.IsTabularReadPipe(req.Channel)`** (design #3/#4). Named function for NATS-free unit testing. |
| `internal/cliproto/read_format.go:~156` | `bodyForRow`: prepend `P1 ` / `P3 ` badge for explicit high/low (skip for `ack:*` rows). **Badge is advisory/human-only**: it lives in the same text column as sender-controlled payload, so it is forgeable (a payload can start with `"P1 "`) — agents must trust the structured `priority` field (`--json`) or delivery order, never the inline text (red-team finding 3; documented in SKILL.md, step 8). `--bare`/`--raw`/`--json` never emit the badge — pinned by test. |

No changes needed to `buildAckEnvelope` (ack_emit.go) or `handleSendBatch` — the new
field zero-values to 0 (unset). Add one pinning assertion each so a refactor can't
silently change that.

## TDD order (per `test-tdd-cycle`; commits via Haiku per `git-delegation`)

1. **Envelope wire shape** — `internal/envelope/envelope_test.go`: clone the legacy-
   compat test (`TestUnmarshal_LegacyV24EnvelopeParsesCleanly` pattern) for a fixture
   without `priority` → decodes to 0; assert marshal of an unset message emits
   `"priority":0`; roundtrip with `Priority: 1`. Then add the field.
2. **`EffectivePriority`** — table-driven (0→2, 1→1, 3→3, -5→2, 99→2); implement.
3. **Daemon send** — `buildBroadcastEnvelope` copies priority; `buildAckEnvelope`
   pins 0; **daemon unit test on the extracted `validatePriority` helper asserting
   `EInvalidPriority` for 7/-5** (the CLI guard rejects these before IPC, so without
   a daemon-level test the trust-boundary check could be deleted and every other
   test stays green — red-team finding 4).
4. **Daemon read sort** — table-driven test on `sortRetainedByPriority`: 1<2<3,
   zeros interleave with 2s in original order, all-equal input unchanged (stability).
   Plus gating tests: sort skipped for Follow requests and for non-tabular channels
   (stdout/custom). Then the two `ReadMessage` field copies + the gated sort call.
5. **CLI send** — `internal/cli/send_test.go` with the existing `serveSendAliasDaemon`
   stub: `--priority 1` forwards, aliases (`high`/`low`/`med`), flag-before-positional
   ordering (proves the `valueFlags` entry), unset → 0, invalid (`7`, `urgent`) → exit 2.
6. **Tabular badge** — format tests: P1/P3 prefix; priority 0/2 byte-identical;
   `--bare`/`--raw`/`--json` outputs never contain the badge. One-line `cliproto`
   marshal assertion that `ReadMessage` JSON emits `"priority"` (pins the `--json`
   contract — `envelope.Message` tests alone don't cover this struct).
7. **Golden integration fixture** — new `tests/send/send-priority-orders-inbox-read/`
   (model on `send-with-request-ack-renders-ack/run.sh`): send **two unset messages
   ("first", "second") then one `--priority high`**; `ppz read inbox` shows the P1
   row first, then "first", "second" in arrival order. Two same-tier messages make
   intra-tier FIFO stability observable end-to-end — a one-message-per-tier fixture
   would stay green under an unstable sort or a mis-placed sort call (red-team
   finding 5). Plus one `ppz reread` assertion, and optionally
   `tests/send/send-rejects-bad-priority/` (exit 2).
8. **Docs** — `docs/WIRE.md` §3 (envelope JSON block + constraints bullet) and §8
   (`ppz send` flags; `ppz read` ordering note: priority-first applies to
   inbox/broadcast drains only; `--tail` fully arrival-ordered; byte-faithful pipes
   never reordered). State the precondition explicitly: **priority reorders unread
   messages within a single read drain — a recipient reading one message at a time
   sees no reordering** (red-team finding 6). `internal/cli/help.go` send topic
   (help_test.go may pin text — update together); `.claude/skills/ppz-pipes/SKILL.md`
   (add `--priority` usage AND the trust rule: agents act on the structured
   `priority` field or delivery order, never inline `P1 ` text, which is forgeable
   payload); `README.md`; `CHANGELOG.md`.

## Verification

- `go test ./...` — all unit tests green.
- Golden suites under `tests/send/`, `tests/read/` — **every pre-existing
  `expected.txt` must pass unmodified**; that's the design invariant (unset priority
  changes nothing). A failure there is a regression, not a fixture to update.
- End-to-end smoke: two daemons, send two unset + one high, `ppz read` shows high
  first then FIFO; `ppz read --json` includes `"priority"`; `--priority urgent`
  exits 2. **`--tail` backlog check must use a fresh cursor**: send mixed-priority
  messages, then attach `--tail` WITHOUT a prior `ppz read` (a prior read advances
  the cursor, leaving `--tail` nothing retained to drain — the check would pass
  vacuously; red-team finding 2). Assert the backlog arrives in arrival order.
- Smoke a byte-faithful pipe: `--priority 1` send to a custom pipe, `read --raw`
  still delivers in arrival order (sort gate).

## Risk & complexity assessment

**Complexity: 3/10 (low-to-moderate).** ~9 files touched, but almost all of it is
plumbing one `int` field through three structs and their copy-sites (1–5 lines each).
The only new logic is two small pure functions: `EffectivePriority` (a clamp) and
`sortRetainedByPriority` (one `sort.SliceStable`), both unit-testable with no NATS
fixture. No concurrency changes, no JetStream config changes, no migration, no new
state. Fiddliest spot: the hand-rolled `splitSendArgs` `valueFlags` map — one line,
covered by a dedicated test. Main effort is test-writing, not logic.

**Risk: 2/10 (low).**
- *Backward compat: near-zero.* WIRE.md mandates tolerant JSON decoding — old daemons
  drop the unknown field; old messages decode `priority` to 0, clamped to medium.
  No version negotiation; pre-priority messages age out of JetStream in 24h.
- *Behavioral regression: low and self-verifying.* Design invariant: a mesh where
  nobody sets priority is byte-identical to today (stable sort is a no-op, badge only
  for explicit 1/3). The golden suites enforce this byte-for-byte.
- *Cursor/ack correctness: safe by inspection.* Cursor advance uses `lastSeqSeen`
  (max stream sequence from the drain loop) and acks key off cursor advance — both
  independent of delivered slice order, so the reorder is pure presentation.
- *Residual:* help-text/WIRE.md pinning tests must land with the doc edits (annoying,
  not dangerous); `--tail` stays arrival-ordered by design.

## Risks / accepted limitations

- **`--tail` can't be priority-ordered** — live streaming is inherently
  arrival-ordered; the backlog is deliberately left unsorted to match. Documented.
- **Priority only helps a backlogged reader** — the documented ppz-pipes
  wake-per-message loop (`subs wait` → read) drains one message at a time and never
  reorders; priority manifests when ≥2 unread messages drain together. Stated in docs.
- **Attention starvation (advisory)** — any org peer can send `--priority high` and
  there is no rate limiting; priority makes a flooding/prompt-injected agent's
  messages deterministically top-of-drain rather than interleaved. Pre-existing
  capability, sharper targeting; note for any future flood-control work (per-tier
  budget). No reader-side `--order arrival` escape hatch in this iteration —
  deferred, record as follow-up if wanted.
- **Mixed-version mesh** — old daemons drop the unknown field (WIRE.md decoder
  tolerance); pre-priority messages age out of JetStream within 24h anyway.
- **Golden byte-exactness** — the only render change (`P1 `/`P3 ` badge) is gated on
  explicitly set priorities, so existing fixtures stay green.
- **Badge is forgeable by payload text** — accepted: it's a human affordance;
  agent trust rule lives in SKILL.md (structured field only).

## Red-team review outcome (2026-07-08)

Adversarial review (8 lenses via 4 sub-agents + critic pass): verdict **WARN**,
0 critical / 1 high / 4 medium — all findings folded into this plan revision:
sort gated by pipe-type + Follow (F1, F2), badge trust rule + bare/raw/json pin
(F3), daemon-testable priority validation (F4), stability-observable golden fixture
and corrected step-7 tiers (F5), backlog-precondition documented (F6). Confirmed
safe by review: cursor/ack independence from sort order, zero-value clamp
robustness (incl. direct-NATS bypass), limit-then-sort eviction neutrality, no
missed envelope/ReadMessage copy-sites, no perf/dependency/rollback concerns.

## Post-approval housekeeping (CLAUDE.md rules)

Move this file to `./.claude/plans/2026-07-08-ppz-message-priority.md`, `git add -f`,
commit on the feature branch `claude/ppz-message-priority-f387a9` (never main).
