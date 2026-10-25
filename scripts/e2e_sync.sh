#!/bin/bash
# End to end test of directory sync between 3 tsync instances (A, B, C) on this machine, in tmux: separate
# identities and state (-tsync-dir), own port and multicast group (so a real tsync doesn't see them), each with
# a "docs" sync folder. Scenarios: initial sync, an edit, a deletion and a directory rename going around, an
# offline machine (C stopped) edited on both sides then back (conflict copy, the newest version wins), and a
# mass deletion while offline (held by the others, not applied), and a machine restarted headless.
# Needs tmux and multicast on the local interface.
# Usage: scripts/e2e_sync.sh [outdir] (default: a new temp dir, kept on failure)
set -euo pipefail

OUT=${1:-$(mktemp -d -t tsync-e2e-sync)}
mkdir -p "$OUT/A" "$OUT/B" "$OUT/C"
BIN="$OUT/tsync"
go build -o "$BIN" .
ARGS="-port 29560 -mcast 239.255.115.119 -sync-rescan 300ms"
T="tmux -L tsync-e2e-sync -f /dev/null"
cleanup() {
	$T kill-server 2>/dev/null || true
}
trap cleanup EXIT
cleanup

fail() {
	echo "FAIL: $*" >&2
	for s in A B C; do
		echo "--- screen $s:" >&2
		$T capture-pane -p -t $s >&2 || true
	done
	echo "(outputs in $OUT)" >&2
	exit 1
}

# wait_for SESSION FIXED_STRING [TIMEOUT_SECONDS]: waits for the string on the session's screen.
wait_for() {
	local deadline=$((SECONDS + ${3:-15}))
	while ! $T capture-pane -p -t "$1" | grep -qF -- "$2"; do
		if [ $SECONDS -ge $deadline ]; then
			fail "timeout waiting for '$2' on $1"
		fi
		sleep 0.2
	done
	echo "ok: $1 shows '$2'"
}

# wait_until DESCRIPTION COMMAND...: waits (30s at most) for the command to succeed.
wait_until() {
	local what=$1 deadline=$((SECONDS + 30))
	shift
	while ! "$@" 2>/dev/null; do
		if [ $SECONDS -ge $deadline ]; then
			fail "timeout waiting for $what"
		fi
		sleep 0.2
	done
	echo "ok: $what"
}

# has FILE CONTENT...: all the files (one per machine, same relative path) have that content.
has() {
	local content=$1
	shift
	for f in "$@"; do
		[ "$(cat "$f" 2>/dev/null)" = "$content" ] || return 1
	done
}

# gone FILE...: none of the files exist.
gone() {
	for f in "$@"; do
		[ ! -e "$f" ] || return 1
	done
}

on() { # on PATH: the path on all 3 machines.
	echo "$OUT/A/$1" "$OUT/B/$1" "$OUT/C/$1"
}

start() {
	$T new-session -d -s "$1" -x 200 -y 50 \
		"$BIN $ARGS -name e2e$1 -tsync-dir $OUT/id$1 -dir $OUT/dl$1 -sync docs=$OUT/$1"
}

stop() {
	$T send-keys -t "$1" q
	wait_until "$1 stopped" bash -c "! $T has-session -t $1"
}

# Initial content on A only, with enough files for the mass deletion guard (more than 20 and half of all).
echo hello > "$OUT/A/readme.txt"
echo v1 > "$OUT/A/notes.txt"
mkdir -p "$OUT/A/src/util" "$OUT/A/bulk"
echo "package main" > "$OUT/A/src/main.go"
echo util > "$OUT/A/src/util/u.go"
for i in $(seq -w 1 40); do echo "$i" > "$OUT/A/bulk/f$i"; done

start A
start B
start C
for s in A B C; do
	for p in A B C; do
		[ $s = $p ] || wait_for $s "e2e$p"
	done
done
# Peers are listed by name (same IP): A sees 1=B 2=C, B sees 1=A 2=C, C sees 1=A 2=B.
connect() { # connect FROM KEY TO KEY: FROM asks (its peer number KEY), TO accepts.
	$T send-keys -t "$1" "$2"
	wait_for "$3" "wants to connect"
	$T send-keys -t "$3" "$4"
	wait_for "$1" "🔗 e2e$3"
	wait_for "$3" "🔗 e2e$1"
}
connect A 1 B 1
connect A 2 C 1
connect B 2 C 2

# 1. Everything from A gets to B and C.
wait_until "initial sync" has "util" $(on src/util/u.go)
wait_until "initial sync (bulk)" has "40" $(on bulk/f40)

# 2. An edit on B goes to A and C (the previous version kept in .tsync-old).
echo "v2 from B" > "$OUT/B/notes.txt"
wait_until "edit propagated" has "v2 from B" $(on notes.txt)
ls "$OUT/A/.tsync-old/notes.txt~"* > /dev/null || fail "replaced version not kept on A"

# 3. A deletion on C is done on A and B (moved to .tsync-old).
rm "$OUT/C/readme.txt"
wait_until "deletion propagated" gone $(on readme.txt)
ls "$OUT/A/.tsync-old/readme.txt~"* "$OUT/B/.tsync-old/readme.txt~"* > /dev/null || fail "deleted file not kept"

# 4. A directory renamed on A is renamed on B and C (not transferred nor kept as deleted).
mv "$OUT/A/src" "$OUT/A/code"
wait_until "rename propagated" has "util" $(on code/util/u.go)
wait_until "old directory removed" gone $(on src)
[ ! -e "$OUT/B/.tsync-old/src" ] || fail "renamed files shouldn't be kept as deleted on B"

for s in A B C; do
	for p in A B C; do
		[ $s = $p ] || wait_for $s "📁 docs ↔ e2e$p · in sync"
	done
done
echo "ok: all in sync"

# 5. C goes offline, notes.txt is edited there (older) and on A (newer): back online, A's version wins, C's is
# kept as a conflict copy, on all machines.
stop C
echo "offline edit on C" > "$OUT/C/notes.txt"
touch -t 202601010000 "$OUT/C/notes.txt"
echo "v3 from A" > "$OUT/A/notes.txt"
wait_until "edit while C is offline" has "v3 from A" "$OUT/B/notes.txt"
start C
wait_for C "🔗 e2eA" 20
wait_until "conflict resolved" has "v3 from A" $(on notes.txt)
copy=$(cd "$OUT/C" && ls notes.sync-conflict-e2eC-*.txt) || fail "no conflict copy on C"
wait_until "conflict copy on all machines" has "offline edit on C" $(on "$copy")
wait_for A "1 conflict copy to review" 20
grep -q "^conflict copy	$copy	" "$OUT/idA/sync/docs/conflicts.txt" || fail "conflict copy not listed on A"

# 6. The reviewed conflict copy is deleted on B: gone everywhere.
rm "$OUT/B/$copy"
wait_until "conflict copy deleted everywhere" gone $(on "$copy")
wait_until "conflicts list removed" gone "$OUT/idA/sync/docs/conflicts.txt"

# 7. C offline again, (almost) everything deleted there by mistake (eg not mounted): back online, A and B hold
# those deletions (too many at once) instead of following them.
stop C
rm -rf "$OUT/C/bulk" "$OUT/C/code"
start C
wait_for A "deletions on e2eC held (too many at once)" 20
wait_for B "deletions on e2eC held (too many at once)" 20
sleep 2 # a few rescans later, still there:
has "40" "$OUT/A/bulk/f40" "$OUT/B/bulk/f40" || fail "mass deletion shouldn't be applied"
grep -q "^deletion held	bulk/f01	" "$OUT/idA/sync/docs/conflicts.txt" || fail "held deletions not listed on A"
echo "ok: mass deletion held"

# 8. C restarted headless (no UI, already trusted peers only): reconnects by itself and syncs, stops on Ctrl-C.
stop C
echo "for headless" > "$OUT/A/headless.txt"
$T new-session -d -s C -x 200 -y 50 \
	"$BIN $ARGS -headless -name e2eC -tsync-dir $OUT/idC -dir $OUT/dlC -sync docs=$OUT/C"
wait_until "headless sync" has "for headless" $(on headless.txt)
echo "from headless" > "$OUT/C/from-headless.txt"
wait_until "headless change propagated" has "from headless" $(on from-headless.txt)
$T send-keys -t C C-c
wait_until "headless stopped by Ctrl-C" bash -c "! $T has-session -t C"
echo "ok: headless"

for s in A B; do
	$T capture-pane -p -t $s > "$OUT/$s.txt"
done
echo "PASS (screens in $OUT)"
