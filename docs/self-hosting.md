# Self-hosting a local ppz server

Run the full ppz stack yourself — `ppz-server` with embedded NATS/JetStream
backed by Postgres — with no dependency on pipescloud.io. This is the long-form
companion to the [Self-hosting](../README.md#self-hosting) section of the README.

Three setup paths are documented, easiest first: the interactive helper
(**Path A**), Docker Compose (**Path B**), and the raw native binary
(**Path C**). All three target `ppz 0.45.1`.

---

## When you actually need this

You only need a local server to run a **private mesh** with no dependency on
pipescloud.io — e.g. an air-gapped LAN, CI, or e2e tests. For normal multi-agent
coordination you just `ppz login pipescloud.io` and use the hosted server. Don't
self-host unless you have a reason to.

---

## Architecture (what "the server" is)

```
ppz CLI / daemon ──HTTP :8080──▶ ppz-server ──┬─▶ Postgres   (accounts, sources, pipes, api_keys, …)
        │                                       └─▶ embedded NATS/JetStream  (:4222, message transport)
        └────────────────── NATS :4222 ────────────────▲
```

`ppz-server` is a single Go binary that:
- owns account/source/pipe state in **Postgres** (auto-migrates on boot),
- **embeds its own NATS/JetStream** server (no separate `nats-server` needed),
- serves the HTTP API + web GUI on `:8080`,
- hands clients a NATS URL at auth time, derived from the request `Host`
  (hit it on `localhost:8080` → you're told `nats://localhost:4222`).

It needs a one-time **NATS NSC/JWT credential chain**, minted by
`ppz-natsbootstrap`. Auth is GitHub OAuth in production, with a `dev-login`
bypass for local/test use.

---

## Prerequisites

1. **The server binaries.** The default install is client-only. Re-run the
   installer opting in:
   ```bash
   PPZ_INCLUDE_SERVER=1 ./install.sh        # adds ppz-server + ppz-natsbootstrap
   # or, from a repo clone (also builds ppz-seed, needed for dev-login fixtures):
   make install                              # builds + installs all five binaries to ~/.local/bin
   ```
   Confirm: `which ppz-server ppz-natsbootstrap`. Note `install.sh` does **not**
   ship `ppz-seed` — it comes only from the repo-clone path above, so use that if
   you want the dev-login seed fixtures.
2. **Postgres 16** reachable. No native Postgres? Run one in Docker (below).
3. **Docker** (only if you take the Compose path or the Docker-Postgres path).

---

## Path A — `scripts/ppz-local-server.sh` (recommended)

The repo ships an interactive helper that automates the whole native setup
(**Path C**): it asks a handful of questions, provisions everything, and caches
the config under `.ppz-local/` (gitignored, `0600`). From a repo clone:

```bash
scripts/ppz-local-server.sh          # interactive setup (first run)
scripts/ppz-local-server.sh --start  # load the saved config + run ppz-server
# Log a CLI in. For the default dev-login setup this is the SEEDED API KEY —
# a bare `ppz login URL` would use the GitHub device flow, which a dev-login
# server has no app for (see Auth + the logout warning):
ppz login http://localhost:8080 -apikey "$(cat .ppz-local/seed/key-alpha.txt)"
```

The helper prints this exact command (with the resolved key path) under
**Next steps** after setup, and again in report mode — copy-paste it.

It walks you through, then does the work:

- **Binaries** — offers to `make build` if `ppz-server` / `ppz-natsbootstrap` /
  `ppz-seed` are missing.
- **Postgres** — spins up a managed `postgres:16-alpine` container for you, or
  takes a connection URL you supply.
- **NATS trust root** — runs `ppz-natsbootstrap` and **caches** the operator /
  system JWTs to `.ppz-local/nats-bootstrap.env`, so a restart doesn't rotate
  the Operator key (see the credential warning below).
- **JetStream** store dir, a generated **session key**, and **auth** (GitHub
  OAuth or dev-login), plus optional seed fixtures.

Re-run it with no arguments and it **reports** the current config instead of
reconfiguring — secrets masked, a NATS operator-JWT fingerprint, and a live
`/healthz` probe:

```bash
scripts/ppz-local-server.sh                 # report mode (already configured)
scripts/ppz-local-server.sh --reconfigure   # change settings
```

> **Reconfiguration warning** — choosing to regenerate the NATS credentials
> during `--reconfigure` mints a new Operator key and **invalidates every login
> already issued**; all clients must `ppz login` again. The helper defaults to
> *keeping* existing creds and makes you opt in behind that warning. Changing
> DB / ports / auth alone is safe.

Want full container isolation instead? Use **Path B** (Compose). Want to drive
every knob by hand and understand the moving parts? See **Path C** (native) —
the path this script automates.

---

## Path B — Docker Compose (fully isolated)

The repo ships a complete stack. From the repo root:

```bash
make compose-up            # builds + starts: postgres, mock-github, ppz-server (+ daemons, GUIs)
# server:        http://localhost:8080   (GUI + HTTP API)
# embedded NATS: nats://localhost:4222
# GUI viewers:   http://localhost:9091 (daemon-a), :9092 (daemon-b)
make compose-down          # tear down + delete volumes (-v)
```

This path sets `PPZ_DEV_LOGIN=true` and seeds users, so you can authenticate
without a real GitHub app (see **Auth** below). It's the right choice for
"I just want a server to point a CLI at" and for the e2e suite (`make e2e`).
It is **isolated** — it does not touch your host `ppz` daemon unless you
deliberately `ppz login` against it.

> The Compose entrypoint caches the bootstrap creds to the seed volume on first
> boot and *reuses* them across `docker stop`/`start`. This is deliberate: the
> NATS Operator key signs every account/user JWT, so regenerating it on restart
> would invalidate all issued creds ("Authorization Violation" on reconnect).
> `make compose-down` (with `-v`) wipes the volume → fresh keys next time.

---

## Path C — Native binary + Postgres

Run the installed `ppz-server` directly. This is the minimal, no-Compose path.

### 1. Postgres
```bash
docker run -d --name ppz-pg -e POSTGRES_PASSWORD=ppz -e POSTGRES_DB=ppz \
  -p 5432:5432 postgres:16-alpine
# wait until ready:
until docker exec ppz-pg pg_isready -U postgres -d ppz; do sleep 1; done
```

### 2. Mint NATS credentials (one-shot)
`ppz-natsbootstrap` prints exactly three `export`-able lines:
```bash
ppz-natsbootstrap > /tmp/ppz-nats.env
cut -d= -f1 /tmp/ppz-nats.env
# PPZ_NATS_OPERATOR_JWT
# PPZ_NATS_OPERATOR_SEED        <- the server HARD-FAILS to boot without this
# PPZ_NATS_SYSTEM_ACCOUNT_JWT
```
Treat these as secrets (the seed is a signing key). **Persist this file** —
regenerating it rotates the Operator key and invalidates every credential the
server has already issued.

### 3. Run the server
```bash
set -a; . /tmp/ppz-nats.env; set +a          # load the 3 NATS vars
export PPZ_DB_URL="postgres://postgres:ppz@localhost:5432/ppz?sslmode=disable"
export PPZ_HTTP_ADDR=":8080"
export PPZ_NATS_ADDR="127.0.0.1:4222"        # bind an explicit host; a bare ":4222" resolves to 0.0.0.0, which the server can't dial back on macOS (account provisioning 500s)
export PPZ_BASE_URL="http://localhost:8080"
export PPZ_SESSION_KEY="some-32+-byte-random-string-here"   # rotating it invalidates sessions
export PPZ_DEV_LOGIN="true"                  # enable /dev/login + seed-key auth (omit in prod)
export PPZ_JETSTREAM_STORE_DIR="$HOME/.local/share/ppz/jetstream"   # durable JetStream storage

ppz-server                                   # migrates Postgres, embeds NATS, listens on :8080
```

On boot you'll see `ppz-server listening on :8080`. The DB is migrated
automatically (tables: `users, accounts, account_members, api_keys, sources,
pipes, oauth_device_codes, oauth_tokens, invites`).

### 4. Verify
```bash
curl -fsS http://localhost:8080/healthz      # -> {"status":"ok","version":"<build version>"}
                                             # version is the -ldflags build tag (e.g. v0.45.1,
                                             # or v0.43.2-3-gc2aa137 for a dev build; "dev" if unset)
lsof -nP -iTCP:4222 -sTCP:LISTEN              # embedded NATS is listening
```

### 5. Teardown
```bash
# Ctrl-C the server, then:
docker rm -f ppz-pg
```

---

## Required / optional environment variables

Read by `cmd/ppz-server` (defaults in parentheses):

| Var | Required? | Purpose |
|---|---|---|
| `PPZ_NATS_OPERATOR_SEED` | **Yes** — hard fail if empty | Operator signing key; signs per-org account JWTs. From `ppz-natsbootstrap`. |
| `PPZ_NATS_OPERATOR_JWT` | Yes | Operator JWT for the embedded NATS. From bootstrap. |
| `PPZ_NATS_SYSTEM_ACCOUNT_JWT` | Yes | NATS system account JWT. From bootstrap. |
| `PPZ_DB_URL` | (default localhost) | Postgres DSN (`postgres://postgres:ppz@localhost:5432/ppz?sslmode=disable`). |
| `PPZ_HTTP_ADDR` | (`:8080`) | HTTP/GUI listen address. |
| `PPZ_NATS_ADDR` | (`:4222`) | Embedded NATS listen address. On macOS set `127.0.0.1:4222` — a bare `:4222` resolves to `0.0.0.0`, which the server then can't dial back when provisioning accounts. |
| `PPZ_SESSION_KEY` | Recommended | Signs web sessions; rotating it invalidates all sessions. |
| `PPZ_BASE_URL` | (`http://localhost:8080`) | Used to build OAuth callback URLs. |
| `PPZ_JETSTREAM_STORE_DIR` | Recommended | On-disk JetStream store; without it messages aren't durable across restarts. |
| `PPZ_NATS_PUBLIC_URL` | Optional | Override the NATS URL advertised to clients (else derived from `Host`). |
| `PPZ_NATS_JWT_TTL` | Optional (`5m`) | Lifetime of minted per-client NATS user JWTs. Only honoured when `PPZ_DEV_LOGIN=true` (test knob). |
| `PPZ_DEV_LOGIN` | Optional | `"true"` enables `POST /dev/login` + admin wipe — local/test only. |
| `PPZ_SEED` / `PPZ_SEED_DIR` | Optional | (entrypoint-level) run the seeder; writes plaintext keys to `SEED_DIR`. |
| `PPZ_GITHUB_CLIENT_ID` / `_SECRET` / `_AUTHORIZE_URL` / `_TOKEN_URL` / `_USER_URL` | Prod auth | GitHub OAuth; URLs default to real github.com when unset. |

---

## Auth: how clients log in to YOUR server

There are three ways, easiest first:

### (a) Dev-login + seeded API keys — no GitHub app (local/test)
Boot with `PPZ_DEV_LOGIN=true` and seed the DB (`PPZ_SEED=true`,
`PPZ_SEED_DIR=/some/dir`, or run `ppz-seed --dir /some/dir`). The seeder creates
users `foo` & `bar` and orgs `alpha` & `beta` (foo owns alpha; bar is a member
of both), and writes **plaintext API keys** to:
```
<seed-dir>/key-alpha.txt   key-alpha2.txt   key-beta.txt
<seed-dir>/user-<name>.txt   org-<name>.txt
```
A client then skips the browser entirely:
```bash
ppz login http://localhost:8080 -apikey "$(cat <seed-dir>/key-alpha.txt)"
```
**This is THE login path for a dev-login server** — there is no GitHub app, so
the browser device flow below (b) is unavailable. A bare `ppz login URL` on a
dev-login server dead-ends: the CLI opens `/oauth/device/verify`, which needs a
browser session; with no session you're bounced to `/login`, whose only button
is *Continue with GitHub* → `github oauth not configured (PPZ_GITHUB_CLIENT_ID)`.
The `scripts/ppz-local-server.sh` helper (seed dir `.ppz-local/seed/`) prints the
exact `-apikey` command for you. If you genuinely want the browser flow, use (b).

### (b) Real GitHub OAuth (a genuine private server)
Register a GitHub OAuth app ("Pipes Local Dev") with callback
**`http://localhost:8080/auth/github/callback`**, then set
`PPZ_GITHUB_CLIENT_ID` / `PPZ_GITHUB_CLIENT_SECRET` and leave the GitHub URLs at
their github.com defaults. Clients use the normal browser device flow:
```bash
ppz login http://localhost:8080            # opens browser; or -no-open to print the URL
```

### (c) GUI (needs GitHub auth)
Browse to `http://localhost:8080/login`, sign in, create an org + API key under
`/dashboard`, then `ppz login http://localhost:8080 -apikey <key>`.

> On a **dev-login** server the browser GUI is effectively unreachable: the
> landing page's only button is *Continue with GitHub* (unconfigured), and
> `/dev/login` is registered `POST`-only — a URL-bar `GET` returns `405`. Use
> mode (a) from the CLI, or configure GitHub OAuth (b) to unlock the GUI.

---

## ⚠️ Pointing your CLI at a local server logs you out of pipescloud.io

`ppz` keeps a **single** credential (`$PPZ_HOME/credentials`, default
`~/.ppz/credentials`). Running
`ppz login http://localhost:8080` **replaces** your pipescloud.io login — your
hosted handle/session is gone until you `ppz login pipescloud.io` again. If you
need both concurrently, isolate the local client with a separate home:
```bash
PPZ_HOME=/tmp/ppz-local ppz login http://localhost:8080 -apikey <key>
PPZ_HOME=/tmp/ppz-local ppz daemon start
PPZ_HOME=/tmp/ppz-local ppz status
```
This keeps the local-server session in its own daemon/socket, leaving your
pipescloud.io daemon untouched.

---

## Gotchas

- **`PPZ_NATS_OPERATOR_SEED` is mandatory** — the server exits immediately
  without it. All three bootstrap vars must be loaded together.
- **Don't regenerate bootstrap creds casually.** A new Operator key invalidates
  every already-issued account/user JWT → clients hit "Authorization Violation"
  on reconnect. Persist `ppz-natsbootstrap` output; only rotate deliberately.
- **macOS: bind NATS to `127.0.0.1`, not `:4222`.** A bare `:4222` makes the
  embedded NATS advertise its own client URL as `nats://0.0.0.0:4222`, which the
  server dials to provision accounts. macOS can't dial `0.0.0.0`, so the first
  `ppz login` 500s (`can't assign requested address`). Set
  `PPZ_NATS_ADDR=127.0.0.1:4222`. (Linux routes `0.0.0.0` to loopback, so it's
  Mac-specific.)
- **NATS URL is derived from `Host`.** Reach the server via `localhost:8080` and
  you'll be handed `nats://localhost:4222`. On odd non-Docker setups where the
  client can't reach NATS, force it with `PPZ_NATS_URL=nats://localhost:4222`
  (this is the fix surfaced in the `E_NATS_UNREACHABLE` error text).
- **JetStream durability needs a store dir.** Without `PPZ_JETSTREAM_STORE_DIR`,
  retained messages won't survive a server restart.
- **`PPZ_DEV_LOGIN=true` exposes admin/wipe endpoints** (`POST /api/v1/admin/wipe`).
  Never set it on anything internet-reachable.
- **Compose vs native ports clash.** Both bind `8080`/`4222`/`5432` — run one or
  the other, not both.
