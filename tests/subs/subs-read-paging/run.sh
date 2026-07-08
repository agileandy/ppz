#!/usr/bin/env bash
# `ppz subs read` forward-pages by DEFAULT (page size 20 per pipe). A flooded
# auto-subscribed inbox is read a page at a time instead of draining the whole
# backlog in one call — which, because `subs read` and `read inbox` share the
# session cursor, would otherwise starve a later `read inbox`. The cursor
# advances only past the delivered page, so the remainder surfaces on the next
# call; a stderr "showing N of M unread" notice flags the truncation.
#
# Flow: flood 30, subs read returns the 20 OLDEST (msg-1..msg-20) + notice,
# second subs read returns the remaining 10 (msg-21..msg-30), no notice.
. /tests/lib/common.sh
ppz_a daemon login "$PPZ_SERVER_URL" -apikey "$(key_alpha)" >/dev/null
export PPZ_SESSION=paging
ppz_a source create flood >/dev/null
for i in $(seq 1 30); do ppz_a send flood.inbox "msg-$i" >/dev/null; done
wait_for 40 "ppz_a ls | grep -q msg-30" >/dev/null

OUT1=$(mktemp); ERR1=$(mktemp)
ppz_a subs read --bare >"$OUT1" 2>"$ERR1"
echo "--- page 1: count / first / last ---"
grep -c '^msg-' "$OUT1"
grep '^msg-' "$OUT1" | head -1
grep '^msg-' "$OUT1" | tail -1
echo "--- page 1 stderr notice ---"
grep -oE 'showing [0-9]+ of [0-9]+ unread' "$ERR1"

OUT2=$(mktemp)
ppz_a subs read --bare >"$OUT2" 2>/dev/null
echo "--- page 2: count / first / last ---"
grep -c '^msg-' "$OUT2"
grep '^msg-' "$OUT2" | head -1
grep '^msg-' "$OUT2" | tail -1
