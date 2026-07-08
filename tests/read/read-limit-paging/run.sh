#!/usr/bin/env bash
# `ppz read --limit N` (a.k.a. -l N) forward-pages the unread window: deliver
# the first N (OLDEST) unread and advance the session cursor only past them, so
# the next `read` returns the remainder rather than the tail being skipped.
# Contrast `ppz reread -l N`, which returns the NEWEST N and never moves the
# cursor. A bare `ppz read` (no flag) stays unlimited / drain-all.
#
# Flow: flood 25. Page 1 (--limit 10) → msg-1..msg-10 + "showing 10 of 25"
# notice. Page 2 (-l 10) → msg-11..msg-20. Page 3 (no flag) drains the rest
# → msg-21..msg-25.
. /tests/lib/common.sh
ppz_a daemon login "$PPZ_SERVER_URL" -apikey "$(key_alpha)" >/dev/null
export PPZ_SESSION=rlimit
ppz_a source create flood >/dev/null
for i in $(seq 1 25); do ppz_a send flood.inbox "msg-$i" >/dev/null; done
wait_for 40 "ppz_a ls | grep -q msg-25" >/dev/null

OUT1=$(mktemp); ERR1=$(mktemp)
ppz_a read --bare --limit 10 flood.inbox >"$OUT1" 2>"$ERR1"
echo "--- page 1 (--limit 10): count / first / last ---"
grep -c '^msg-' "$OUT1"
grep '^msg-' "$OUT1" | head -1
grep '^msg-' "$OUT1" | tail -1
echo "--- page 1 stderr notice ---"
grep -oE 'showing [0-9]+ of [0-9]+ unread' "$ERR1"

OUT2=$(mktemp)
ppz_a read --bare -l 10 flood.inbox >"$OUT2" 2>/dev/null
echo "--- page 2 (-l 10): count / first / last ---"
grep -c '^msg-' "$OUT2"
grep '^msg-' "$OUT2" | head -1
grep '^msg-' "$OUT2" | tail -1

OUT3=$(mktemp)
ppz_a read --bare flood.inbox >"$OUT3" 2>/dev/null
echo "--- page 3 (no flag, drain rest): count / first / last ---"
grep -c '^msg-' "$OUT3"
grep '^msg-' "$OUT3" | head -1
grep '^msg-' "$OUT3" | tail -1
