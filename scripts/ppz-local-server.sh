#!/usr/bin/env bash
#
# ppz-local-server.sh — interactive setup + status utility for a
# self-hosted, local ppz-server.
#
# A local ppz-server needs four things the README leaves implicit:
#   1. Binaries          — ppz-server, ppz-natsbootstrap, ppz-seed
#   2. NATS trust root    — ppz-natsbootstrap mints the operator/system
#                           JWTs the embedded NATS authenticates against.
#                           PPZ_NATS_OPERATOR_SEED is *required* at boot.
#   3. Postgres           — account / source / pipe state
#   4. A session key + auth config (GitHub OAuth, or dev-login for tests)
#
# This script asks the relevant questions once, performs the setup, and
# caches everything under ./.ppz-local/ (gitignored). Run it again with
# no flags and it REPORTS the current config instead — including a loud
# warning that regenerating the NATS credentials invalidates every login
# already issued (clients would hit "Authorization Violation" and must
# `ppz login` again).
#
# Usage:
#   scripts/ppz-local-server.sh                first run → setup, later → report
#   scripts/ppz-local-server.sh --reconfigure  force the interactive setup again
#   scripts/ppz-local-server.sh --start        load config + run the server
#   scripts/ppz-local-server.sh --help
#
set -euo pipefail

# ── locate the repo + binaries ──────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BIN_DIR="$REPO_ROOT/bin"

CONFIG_DIR="$REPO_ROOT/.ppz-local"
SERVER_ENV="$CONFIG_DIR/server.env"   # PPZ_* runtime env for ppz-server
NATS_ENV="$CONFIG_DIR/nats-bootstrap.env"  # the precious operator creds
META_ENV="$CONFIG_DIR/meta.env"        # this script's bookkeeping

# ── pretty output (degrade gracefully when not a tty) ───────────────
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
  BOLD="$(tput bold)"; RED="$(tput setaf 1)"; GRN="$(tput setaf 2)"
  YEL="$(tput setaf 3)"; CYN="$(tput setaf 6)"; DIM="$(tput dim)"; RST="$(tput sgr0)"
else
  BOLD=""; RED=""; GRN=""; YEL=""; CYN=""; DIM=""; RST=""
fi
say()  { printf '%s\n' "$*"; }
hdr()  { printf '\n%s%s%s\n' "$BOLD" "$*" "$RST"; }
ok()   { printf '%s✓%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '%s!%s %s\n' "$YEL" "$RST" "$*"; }
err()  { printf '%s✗ %s%s\n' "$RED" "$*" "$RST" >&2; }
die()  { err "$*"; exit 1; }

# ── interactive helpers ─────────────────────────────────────────────
ask() { # ask "prompt" "default" VARNAME
  local prompt="$1" def="${2:-}" __out="$3" reply=""
  if [ -n "$def" ]; then printf '%s%s%s [%s]: ' "$CYN" "$prompt" "$RST" "$def" >&2
  else printf '%s%s%s: ' "$CYN" "$prompt" "$RST" >&2; fi
  read -r reply || true
  [ -z "$reply" ] && reply="$def"
  printf -v "$__out" '%s' "$reply"
}
ask_secret() { # ask_secret "prompt" VARNAME
  local prompt="$1" __out="$2" reply=""
  printf '%s%s%s: ' "$CYN" "$prompt" "$RST" >&2
  read -rs reply || true; printf '\n' >&2
  printf -v "$__out" '%s' "$reply"
}
ask_yesno() { # ask_yesno "prompt" "Y|N"  → returns 0 for yes
  local prompt="$1" def="${2:-Y}" reply="" hint
  [ "$def" = "Y" ] && hint="Y/n" || hint="y/N"
  printf '%s%s%s [%s]: ' "$CYN" "$prompt" "$RST" "$hint" >&2
  read -r reply || true
  [ -z "$reply" ] && reply="$def"
  case "$reply" in [Yy]*) return 0;; *) return 1;; esac
}
choose() { # choose VARNAME "prompt" "opt1" "opt2" ...
  local __out="$1" prompt="$2"; shift 2
  local opts=("$@") i reply
  say "$CYN$prompt$RST" >&2
  for i in "${!opts[@]}"; do printf '  %s) %s\n' "$((i+1))" "${opts[$i]}" >&2; done
  while :; do
    printf 'Select [1-%s]: ' "${#opts[@]}" >&2
    read -r reply || true
    if [[ "$reply" =~ ^[0-9]+$ ]] && [ "$reply" -ge 1 ] && [ "$reply" -le "${#opts[@]}" ]; then
      printf -v "$__out" '%s' "$reply"; return 0
    fi
    warn "Enter a number 1-${#opts[@]}." >&2
  done
}
mask() { # mask a secret, keep a hint of the head/tail
  local s="$1"; local n=${#s}
  if [ "$n" -le 8 ]; then printf '********'; else printf '%s…%s' "${s:0:4}" "${s: -4}"; fi
}
kv() { # kv KEY VALUE → KEY="escaped value"  (safe to `source`; values may contain spaces)
  local k="$1" v="$2"
  v="${v//\\/\\\\}"; v="${v//\"/\\\"}"; v="${v//\$/\\\$}"; v="${v//\`/\\\`}"
  printf '%s="%s"\n' "$k" "$v"
}
port_of() { printf '%s' "${1##*:}"; }  # ":8080" → "8080"

# ── binaries ────────────────────────────────────────────────────────
need_bins=(ppz-server ppz-natsbootstrap ppz-seed)
bins_present() { local b; for b in "${need_bins[@]}"; do [ -x "$BIN_DIR/$b" ] || return 1; done; return 0; }
ensure_binaries() {
  if bins_present; then ok "binaries present in $BIN_DIR"; return; fi
  warn "missing server binaries (${need_bins[*]})"
  if command -v make >/dev/null 2>&1 && command -v go >/dev/null 2>&1; then
    if ask_yesno "Build them now with 'make build'?" Y; then
      ( cd "$REPO_ROOT" && make build )
      bins_present || die "build finished but binaries still missing — check 'make build' output"
      ok "built binaries"
    else
      die "cannot continue without binaries; run 'make build' first"
    fi
  else
    die "binaries missing and Go/make not found — install Go 1.22+ then run 'make build'"
  fi
}

# ── postgres (docker) ───────────────────────────────────────────────
pg_container_state() { # echoes: running | stopped | absent
  command -v docker >/dev/null 2>&1 || { echo absent; return; }
  if docker inspect -f '{{.State.Running}}' "$1" >/dev/null 2>&1; then
    [ "$(docker inspect -f '{{.State.Running}}' "$1")" = "true" ] && echo running || echo stopped
  else echo absent; fi
}
pg_docker_ensure() { # pg_docker_ensure <container> <port>
  local c="$1" p="$2" state; state="$(pg_container_state "$c")"
  case "$state" in
    running) ok "postgres container '$c' already running";;
    stopped) say "starting existing postgres container '$c'…"; docker start "$c" >/dev/null; ok "started";;
    absent)
      say "creating postgres container '$c' (postgres:16-alpine) on :${p}…"
      docker run -d --name "$c" \
        -e POSTGRES_PASSWORD=ppz -e POSTGRES_DB=ppz \
        -p "$p:5432" postgres:16-alpine >/dev/null
      ok "created";;
  esac
  printf 'waiting for postgres to accept connections'
  local i
  for i in $(seq 1 60); do
    if docker exec "$c" pg_isready -U postgres -d ppz >/dev/null 2>&1; then printf ' ready\n'; return 0; fi
    printf '.'; sleep 1
  done
  printf '\n'; die "postgres did not become ready in 60s (check: docker logs $c)"
}

# ════════════════════════════════════════════════════════════════════
#  SETUP
# ════════════════════════════════════════════════════════════════════
cmd_setup() {
  hdr "ppz local server — setup"
  ensure_binaries
  mkdir -p "$CONFIG_DIR"

  # — endpoints —
  hdr "1. Server endpoints"
  local http_addr nats_addr base_url
  ask "HTTP / GUI listen address" ":8080" http_addr
  # Bind NATS to an explicit host, not the bare ":4222". The embedded
  # server reports its bind address back as the client URL the server
  # itself dials to provision accounts; a bare port resolves the host to
  # 0.0.0.0, which is undialable on macOS ("can't assign requested
  # address"). 127.0.0.1 works everywhere and stays loopback-only.
  ask "Embedded NATS listen address" "127.0.0.1:4222" nats_addr
  ask "Base URL (used to build OAuth callbacks + device-verify links)" "http://localhost:$(port_of "$http_addr")" base_url

  # — auth —
  hdr "2. Authentication"
  say "How will users log the CLI in (ppz login)?"
  local auth_choice auth_mode="dev"
  choose auth_choice "Choose an auth mode" \
    "Dev-login  — no GitHub; approve via /dev/login?user=foo in the browser (test/CI; needs seed users)" \
    "GitHub OAuth — real device-flow login against github.com (you supply a GitHub OAuth app)"
  local gh_id="" gh_secret=""
  if [ "$auth_choice" = "2" ]; then
    auth_mode="github"
    say "$DIM Create an OAuth app at https://github.com/settings/developers$RST"
    say "$DIM Authorization callback URL: ${base_url}/auth/github/callback$RST"
    ask        "GitHub OAuth Client ID" "" gh_id
    ask_secret "GitHub OAuth Client Secret" gh_secret
    [ -n "$gh_id" ] && [ -n "$gh_secret" ] || die "GitHub mode needs both Client ID and Secret"
  fi

  # — postgres —
  hdr "3. Postgres"
  local pg_choice pg_mode db_url pg_container="" pg_port=""
  choose pg_choice "Where should account/pipe state live?" \
    "Docker — let this script run a postgres:16-alpine container for you" \
    "Existing Postgres — I'll give you a connection URL"
  if [ "$pg_choice" = "1" ]; then
    pg_mode="docker"
    command -v docker >/dev/null 2>&1 || die "Docker not found on PATH — pick the 'Existing Postgres' option instead"
    ask "Container name" "ppz-postgres" pg_container
    ask "Host port to publish" "5432" pg_port
    db_url="postgres://postgres:ppz@localhost:${pg_port}/ppz?sslmode=disable"
  else
    pg_mode="external"
    ask "PPZ_DB_URL" "postgres://postgres:ppz@localhost:5432/ppz?sslmode=disable" db_url
  fi

  # — seed —
  hdr "4. Test fixtures"
  say "$DIM Seeding creates the OSS demo users (foo/bar) + accounts (alpha/beta).$RST"
  local seed="no"
  local seed_default="N"; [ "$auth_mode" = "dev" ] && seed_default="Y"
  if [ "$auth_mode" = "dev" ]; then
    say "${YEL} Dev-login signs in as a seeded user, so seeding is recommended for this mode.$RST"
  fi
  if ask_yesno "Seed demo users/accounts?" "$seed_default"; then seed="yes"; fi

  # — jetstream —
  hdr "5. Durability"
  local js_dir
  ask "JetStream store directory (pipe data; blank = in-memory, lost on restart)" "$CONFIG_DIR/jetstream" js_dir

  # — session key —
  local session_key=""
  if [ -s "$SERVER_ENV" ]; then
    # shellcheck disable=SC1090
    session_key="$( . "$SERVER_ENV" 2>/dev/null; printf '%s' "${PPZ_SESSION_KEY:-}" )"
  fi
  if [ -z "${session_key:-}" ]; then
    if command -v openssl >/dev/null 2>&1; then session_key="$(openssl rand -base64 32)"
    else session_key="$(head -c 32 /dev/urandom | base64)"; fi
  fi

  # — NATS operator credentials (the destructive bit) —
  hdr "6. NATS trust root"
  local regen_nats="yes"
  if [ -s "$NATS_ENV" ]; then
    say "${YEL}╭──────────────────────────────────────────────────────────────╮$RST"
    say "${YEL}│  Existing NATS operator credentials found.                    │$RST"
    say "${YEL}│  Regenerating them mints a NEW operator key, which            │$RST"
    say "${YEL}│  INVALIDATES every login/JWT already issued. All clients      │$RST"
    say "${YEL}│  would fail to connect and must run 'ppz login' again.         │$RST"
    say "${YEL}╰──────────────────────────────────────────────────────────────╯$RST"
    if ask_yesno "Keep the existing NATS credentials? (recommended)" Y; then regen_nats="no"; fi
  fi
  if [ "$regen_nats" = "yes" ]; then
    say "minting fresh NATS operator/system credentials via ppz-natsbootstrap…"
    umask 077
    "$BIN_DIR/ppz-natsbootstrap" > "$NATS_ENV.tmp"
    mv "$NATS_ENV.tmp" "$NATS_ENV"
    chmod 600 "$NATS_ENV"
    ok "wrote $NATS_ENV"
  else
    ok "kept existing $NATS_ENV"
  fi

  # — perform: postgres + seed —
  hdr "Provisioning"
  if [ "$pg_mode" = "docker" ]; then pg_docker_ensure "$pg_container" "$pg_port"; fi
  [ -n "$js_dir" ] && mkdir -p "$js_dir"
  if [ "$seed" = "yes" ]; then
    say "seeding demo users/accounts…"
    "$BIN_DIR/ppz-seed" --db "$db_url" --dir "$CONFIG_DIR/seed"
    ok "seeded (foo/bar, alpha/beta)"
  fi

  # — write env files —
  umask 077
  {
    echo "# Generated by scripts/ppz-local-server.sh — do not commit."
    echo "# Source this + nats-bootstrap.env, then run ppz-server."
    kv PPZ_DB_URL    "$db_url"
    kv PPZ_HTTP_ADDR "$http_addr"
    kv PPZ_NATS_ADDR "$nats_addr"
    kv PPZ_BASE_URL  "$base_url"
    # Pin the advertised NATS URL for predictable local clients. Blank
    # it to let the server derive it from the request Host header.
    kv PPZ_NATS_PUBLIC_URL "nats://localhost:$(port_of "$nats_addr")"
    kv PPZ_SESSION_KEY "$session_key"
    if [ -n "$js_dir" ]; then kv PPZ_JETSTREAM_STORE_DIR "$js_dir"; fi
    if [ "$auth_mode" = "github" ]; then
      kv PPZ_GITHUB_CLIENT_ID     "$gh_id"
      kv PPZ_GITHUB_CLIENT_SECRET "$gh_secret"
    else
      kv PPZ_DEV_LOGIN "true"
    fi
  } > "$SERVER_ENV"
  chmod 600 "$SERVER_ENV"

  {
    echo "# Bookkeeping for scripts/ppz-local-server.sh — not server env."
    kv PPZ_LOCAL_AUTH_MODE     "$auth_mode"
    kv PPZ_LOCAL_PG_MODE       "$pg_mode"
    kv PPZ_LOCAL_PG_CONTAINER  "$pg_container"
    kv PPZ_LOCAL_PG_PORT       "$pg_port"
    kv PPZ_LOCAL_SEED          "$seed"
    kv PPZ_LOCAL_CONFIGURED_AT "$(date '+%Y-%m-%d %H:%M:%S %z')"
  } > "$META_ENV"
  chmod 600 "$META_ENV"

  ok "configuration written to $CONFIG_DIR"
  print_next_steps "$auth_mode" "$base_url" "$seed"

  if ask_yesno "Start the server now?" Y; then cmd_start; fi
}

print_next_steps() { # print_next_steps <auth_mode> <base_url> <seed>
  local auth_mode="$1" base_url="$2" seed="${3:-no}"
  local keyfile="$CONFIG_DIR/seed/key-alpha.txt"
  hdr "Next steps"
  say "Start the server:   ${BOLD}scripts/ppz-local-server.sh --start${RST}"
  say "Then log the CLI in:"
  if [ "$auth_mode" = "dev" ]; then
    # A dev-login server has NO GitHub app, so the browser device flow
    # (a bare `ppz login URL`) dead-ends at "github oauth not configured".
    # Authenticate with the seeded API key instead — the seed step already
    # wrote it; nothing to create by hand.
    if [ "$seed" = "yes" ]; then
      say "  ${BOLD}ppz login ${base_url} -apikey \"\$(cat ${keyfile})\"${RST}"
      say "  ${DIM}# dev-login server → authenticate with the seeded API key (org alpha).${RST}"
      say "  ${DIM}# Prefix with PPZ_HOME=/tmp/ppz-local to keep a pipescloud.io login intact.${RST}"
    else
      say "  ${YEL}Seeding was skipped, so there's no ready-made API key.${RST} Either:"
      say "    • re-run ${BOLD}--reconfigure${RST} and enable seeding, then use the printed -apikey command, or"
      say "    • open ${BOLD}${base_url}/dev/login?user=<name>${RST} in a browser to mint a GUI session,"
      say "      then create an API key under ${BOLD}/dashboard${RST} and log in with -apikey."
    fi
    say "  ${DIM}A bare 'ppz login ${base_url}' uses the GitHub device flow, which a dev-login${RST}"
    say "  ${DIM}server has no GitHub app for — use the API key above instead.${RST}"
  else
    say "  ${BOLD}ppz login ${base_url}${RST}   ${DIM}# opens GitHub device-flow approval${RST}"
  fi
  say "GUI:  ${BOLD}${base_url}${RST}   ${DIM}(browse here; sign in via /dev/login?user=foo when dev-login is on)${RST}"
}

# ════════════════════════════════════════════════════════════════════
#  START
# ════════════════════════════════════════════════════════════════════
cmd_start() {
  [ -s "$SERVER_ENV" ] && [ -s "$NATS_ENV" ] || die "not configured yet — run: scripts/ppz-local-server.sh"
  ensure_binaries
  # bring postgres up if we manage it
  # shellcheck disable=SC1090
  [ -s "$META_ENV" ] && . "$META_ENV"
  if [ "${PPZ_LOCAL_PG_MODE:-}" = "docker" ]; then
    pg_docker_ensure "${PPZ_LOCAL_PG_CONTAINER}" "${PPZ_LOCAL_PG_PORT}"
  fi
  hdr "Starting ppz-server"
  say "${DIM}env: $SERVER_ENV + $NATS_ENV  ·  Ctrl-C to stop${RST}"
  set -a
  # shellcheck disable=SC1090
  . "$SERVER_ENV"; . "$NATS_ENV"
  set +a
  exec "$BIN_DIR/ppz-server"
}

# ════════════════════════════════════════════════════════════════════
#  REPORT  (default when already configured)
# ════════════════════════════════════════════════════════════════════
cmd_report() {
  hdr "ppz local server — current configuration"
  # shellcheck disable=SC1090
  set -a; . "$SERVER_ENV"; [ -s "$META_ENV" ] && . "$META_ENV"; set +a

  local http_port; http_port="$(port_of "${PPZ_HTTP_ADDR:-:8080}")"
  local nats_port; nats_port="$(port_of "${PPZ_NATS_ADDR:-:4222}")"

  printf '  %-22s %s\n' "Config dir"   "$CONFIG_DIR"
  printf '  %-22s %s\n' "Configured at" "${PPZ_LOCAL_CONFIGURED_AT:-unknown}"
  printf '  %-22s %s\n' "Base URL"     "${PPZ_BASE_URL:-?}"
  printf '  %-22s %s\n' "HTTP / GUI"   "${PPZ_HTTP_ADDR:-?}"
  printf '  %-22s %s\n' "NATS"         "${PPZ_NATS_ADDR:-?}  (advertised: ${PPZ_NATS_PUBLIC_URL:-auto})"
  # DB URL with the password masked
  local db_shown="${PPZ_DB_URL:-?}"
  if [[ "$db_shown" =~ ^([^:]+://[^:]+:)([^@]+)(@.*)$ ]]; then
    db_shown="${BASH_REMATCH[1]}$(mask "${BASH_REMATCH[2]}")${BASH_REMATCH[3]}"
  fi
  printf '  %-22s %s\n' "Postgres"     "$db_shown"
  if [ "${PPZ_LOCAL_PG_MODE:-}" = "docker" ]; then
    printf '  %-22s %s\n' "  ↳ container" "${PPZ_LOCAL_PG_CONTAINER} ($(pg_container_state "${PPZ_LOCAL_PG_CONTAINER}"))"
  fi
  printf '  %-22s %s\n' "JetStream"    "${PPZ_JETSTREAM_STORE_DIR:-(in-memory)}"
  if [ "${PPZ_LOCAL_AUTH_MODE:-}" = "github" ]; then
    printf '  %-22s %s\n' "Auth"        "GitHub OAuth (client $(mask "${PPZ_GITHUB_CLIENT_ID:-}"))"
  else
    printf '  %-22s %s\n' "Auth"        "dev-login (/dev/login?user=…, no GitHub)"
  fi
  printf '  %-22s %s\n' "Seed fixtures" "${PPZ_LOCAL_SEED:-no}"

  # NATS operator credential fingerprint
  if [ -s "$NATS_ENV" ]; then
    local op_jwt; op_jwt="$(grep '^PPZ_NATS_OPERATOR_JWT=' "$NATS_ENV" | head -1 | cut -d= -f2-)"
    printf '  %-22s %s\n' "NATS operator JWT" "$(mask "$op_jwt")  ${GRN}present${RST}"
  else
    printf '  %-22s %s\n' "NATS operator JWT" "${RED}MISSING — run --reconfigure${RST}"
  fi

  # Is it up?
  hdr "Runtime"
  if command -v curl >/dev/null 2>&1 && curl -fsS "http://localhost:${http_port}/healthz" >/dev/null 2>&1; then
    ok "server responding on http://localhost:${http_port}"
  else
    warn "server not responding on http://localhost:${http_port} — start it with --start"
  fi

  # The required warning.
  hdr "${RED}Reconfiguration warning${RST}"
  say "${YEL}Re-running with ${BOLD}--reconfigure${RST}${YEL} lets you change these settings.${RST}"
  say "${YEL}If you choose to ${BOLD}regenerate the NATS credentials${RST}${YEL} during that flow, a"
  say "new operator key is minted and ${BOLD}every existing login/JWT is invalidated${RST}${YEL} —"
  say "all clients (daemons, agents) will fail to connect and must run"
  say "${BOLD}ppz login${RST}${YEL} again. Changing DB / ports / auth alone does NOT break logins;"
  say "only regenerating NATS credentials does.${RST}"

  hdr "Actions"
  say "  ${BOLD}scripts/ppz-local-server.sh --start${RST}        run the server"
  say "  ${BOLD}scripts/ppz-local-server.sh --reconfigure${RST}  change settings (you'll be asked before touching NATS creds)"
  if [ "${PPZ_LOCAL_AUTH_MODE:-}" != "github" ]; then
    if [ "${PPZ_LOCAL_SEED:-no}" = "yes" ]; then
      say "  ${BOLD}ppz login ${PPZ_BASE_URL:-http://localhost:8080} -apikey \"\$(cat ${CONFIG_DIR}/seed/key-alpha.txt)\"${RST}"
      say "    ${DIM}log the CLI in (dev-login server → seeded API key, org alpha)${RST}"
    else
      say "    ${DIM}dev-login server, no seed: mint a session at ${PPZ_BASE_URL:-http://localhost:8080}/dev/login?user=<name>, create a key under /dashboard${RST}"
    fi
  fi
}

# ════════════════════════════════════════════════════════════════════
#  main
# ════════════════════════════════════════════════════════════════════
usage() {
  cat <<EOF
${BOLD}ppz-local-server.sh${RST} — set up / inspect a local ppz-server

  (no args)        first run: interactive setup; afterwards: report config
  --reconfigure    re-run the interactive setup
  --start          load the saved config and run ppz-server
  -h, --help       this help

Config lives in ${CONFIG_DIR} (gitignored).
EOF
}

main() {
  case "${1:-}" in
    -h|--help) usage; exit 0;;
    --start) cmd_start;;
    --reconfigure) cmd_setup;;
    "")
      if [ -s "$SERVER_ENV" ]; then cmd_report; else cmd_setup; fi;;
    *) err "unknown option: $1"; usage; exit 2;;
  esac
}
main "$@"
