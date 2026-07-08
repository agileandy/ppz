#!/usr/bin/env bash
# `ppz read` is cursor-driven. As of forward paging it ACCEPTS -l/--limit
# (deliver the first N OLDEST unread, then advance the cursor past them), but
# still REJECTS the reread-only historical filters --skip/--since — those live
# on `ppz reread` (the forensic replay verb). The split keeps each verb
# single-purpose: read consumes (optionally a page at a time), reread replays.
. /tests/lib/common.sh

ppz_a daemon login "$PPZ_SERVER_URL" -apikey "$(key_alpha)" >/dev/null
ppz_a source create chat >/dev/null
ppz_a send chat.inbox "msg-1" >/dev/null
ppz_a send chat.inbox "msg-2" >/dev/null
ppz_a send chat.inbox "msg-3" >/dev/null
wait_for 20 "ppz_a ls | grep -q msg-3" >/dev/null

echo "--- read -l 2: forward page, succeeds, returns the 2 oldest ---"
ppz_a read --bare chat.inbox -l 2
echo "rc=$?"

echo "--- read --skip 1: reread-only flag, should error, exit nonzero ---"
ppz_a read chat.inbox --skip 1 >/dev/null 2>&1
echo "rc=$?"

echo "--- read --since 1s: reread-only flag, should error, exit nonzero ---"
ppz_a read chat.inbox --since 1s >/dev/null 2>&1
echo "rc=$?"
