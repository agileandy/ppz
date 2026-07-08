# ppz Error Codes & Exit Codes

This table is **authoritative**. CLI exit codes, stderr messages, JSON-RPC
error codes, and HTTP error response codes all derive from it.

## Format

- Stderr (one line, then newline): `error: <CODE>: <message>`
- HTTP body: `{"error": {"code": "<CODE>", "message": "<message>"}}`
- JSON-RPC error: `{"code": <exit_code_int>, "message": "<CODE>: <message>"}`
- Stdout is **never** written when an error occurs.

## Codes

| Exit | Code | When | HTTP status |
|---:|---|---|---:|
| 0  | — | success | 2xx |
| 1  | (none) | unexpected internal error | 500 |
| 2  | (none) | usage / argument parse error | — |
| 10 | `E_NOT_LOGGED_IN` | daemon has no stored credential | — |
| 11 | `E_DAEMON_NOT_RUNNING` | CLI cannot reach the IPC socket | — |
| 12 | `E_INVALID_API_KEY` | server rejected the api key | 401 |
| 13 | `E_SOURCE_TAKEN` | source with this handle already exists in this org | 409 |
| 14 | `E_SOURCE_NOT_FOUND` | no source with this handle in this org | 404 |
| 15 | `E_INVALID_HANDLE` | source handle fails regex or is reserved | 400 |
| 16 | `E_NO_CURRENT_SOURCE` | broadcast attempted with no `current` source set | — |
| 17 | `E_PAYLOAD_TOO_LARGE` | encoded envelope > 65 536 bytes | 413 |
| 18 | `E_SERVER_UNREACHABLE` | daemon could not contact `ppz-server` (HTTP) | 502 |
| 19 | `E_NATS_UNREACHABLE` | daemon could not publish to NATS | — |
| 20 | `E_INVALID_PIPE` | `read`/`send`/`broadcast` target had a malformed or unsupported pipe name | 400 |
| 21 | `E_PIPE_TAKEN` | pipe with this name already exists on this source | 409 |
| 21 | `E_NAME_TAKEN` | user-typed name collides with an existing source, uncollared pipe, or reserved manifold-prefix path at this manifold (Phase 1.5.1 first-wins collision rule) | 409 |
| 22 | `E_PIPE_NOT_FOUND` | no pipe with this name on this source | 404 |
| 23 | `E_INVALID_SUBJECT` | `--subject` value violates a reserved-prefix rule (the `ack:` prefix is daemon-internal) | 400 |
| 24 | `E_INVALID_MANIFOLD` | a manifold (hierarchical-grouping path) segment fails handle validation | 400 |
| 25 | `E_DELIVERY_UNCONFIRMED` | message was published but no JetStream PubAck arrived within the deadline — it may or may not have landed | — |
| 26 | `E_DAEMON_TIMEOUT` | daemon accepted the IPC connection but did not reply within the client deadline | — |
| 27 | `E_INVALID_PRIORITY` | `--priority` value outside `1\|high`, `2\|medium\|med`, `3\|low` (0 = flag omitted; not passable explicitly) | 400 |

`E_PIPE_TAKEN` and `E_NAME_TAKEN` deliberately share exit 21: both mean "the
name you tried to create is already in use", and callers that branch on the
exit code should treat them identically (pick a different name). Scripts that
need to distinguish the two must parse the `<CODE>` token from stderr, which
is stable.

## Standard messages (for stderr)

Codes that reference a specific source/pipe interpolate the offending name
so users see *which* one, not just "source not found". Generic codes (no
entity name available) keep the static message.

| Code | Message format |
|---|---|
| `E_NOT_LOGGED_IN` | `not logged in; run 'ppz daemon login URL -apikey K'` |
| `E_DAEMON_NOT_RUNNING` | `daemon not running; run 'ppz daemon start'` |
| `E_INVALID_API_KEY` | `invalid api key` |
| `E_SOURCE_TAKEN` | `source '<handle>' already exists in this org` |
| `E_SOURCE_NOT_FOUND` | `source '<handle>' not found` |
| `E_INVALID_HANDLE` | `invalid handle '<handle>': must match [a-z0-9-] (max 32, no leading/trailing -, not reserved)` |
| `E_NO_CURRENT_SOURCE` | `no current source for this shell session; run 'ppz source create <handle>' (or 'ppz source switch <handle>' to point at an existing one); if you're driving ppz from agent subprocesses with no shared tty, export PPZ_SESSION=<id> consistently across calls so they share session state` |
| `E_PAYLOAD_TOO_LARGE` | `payload too large; max 64KiB encoded` |
| `E_SERVER_UNREACHABLE` | `server unreachable` |
| `E_NATS_UNREACHABLE` | `nats unreachable; common causes: expired credentials (try 'ppz daemon logout' then re-login), or on non-docker setups missing PPZ_NATS_URL=nats://localhost:4222` |
| `E_INVALID_PIPE` (reserved) | `pipe name '<name>' is reserved` |
| `E_INVALID_PIPE` (regex) | `pipe name '<name>' is invalid: must match [a-z0-9-] (max 32, no leading/trailing -)` |
| `E_INVALID_PIPE` (other) | `invalid pipe; check for typos, or for custom pipes run 'ppz pipe create <handle>.<name>' first` |
| `E_PIPE_TAKEN` | `pipe '<name>' already exists on source '<handle>'` |
| `E_PIPE_NOT_FOUND` | `pipe '<name>' not found on source '<handle>'` |
| `E_INVALID_SUBJECT` | `invalid subject; the 'ack:' prefix is reserved for system-emitted protocol messages` |
| `E_NAME_TAKEN` (source holds the name) | `name '<name>' is already taken by source at <location>` |
| `E_NAME_TAKEN` (uncollared pipe holds the name) | `name '<name>' is already taken by uncollared pipe at <location>` |
| `E_NAME_TAKEN` (manifold-prefix reservation) | `manifold path '<prefix>' is reserved by source '<handle>' at <location>` |
| `E_NAME_TAKEN` (generic, no entity available) | `name already taken by another resource at this manifold` |
| `E_INVALID_MANIFOLD` | `invalid manifold: each dot-separated segment must match [a-z0-9-] (max 32, no leading/trailing -, not reserved)` |
| `E_DELIVERY_UNCONFIRMED` | `delivery unconfirmed; the message was published but the server did not acknowledge it in time — it may or may not have landed; retry if your workflow tolerates a possible duplicate` |
| `E_DAEMON_TIMEOUT` | `daemon did not respond in time; it may be busy or stuck (e.g. mid-restart) — retry, or run 'ppz daemon restart'` |
| `E_INVALID_PRIORITY` | `invalid priority; use 1|high, 2|medium, or 3|low` |

`<location>` in the `E_NAME_TAKEN` variants renders as `root` when the
manifold is empty, otherwise `manifold '<manifold>'`.
