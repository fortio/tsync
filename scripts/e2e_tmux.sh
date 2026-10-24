#!/bin/bash
# End to end test of the TUI: runs 2 tsync instances (separate identities) in tmux,
# connects/accepts, pastes (raw bracketed paste with escape sequences) in A, checks
# B shows the sanitized clip and that `c` on B copies it (OSC 52 -> tmux buffer),
# then that a restarted B auto reconnects (trusted) and gets the last clip again.
# Needs tmux and multicast on the local interface.
# Usage: scripts/e2e_tmux.sh [outdir] (default: a new temp dir, kept on failure)
set -euo pipefail

OUT=${1:-$(mktemp -d -t tsync-e2e)}
mkdir -p "$OUT/hA" "$OUT/hB" "$OUT/dlB" "$OUT/src"
BIN="$OUT/tsync"
go build -o "$BIN" .
# Own port and multicast group so we don't see (or get seen by) a real tsync or the go tests.
ARGS="-port 29558 -mcast 239.255.115.117"
T="tmux -L tsync-e2e -f /dev/null"
cleanup() {
	$T kill-server 2>/dev/null || true
}
trap cleanup EXIT
cleanup

fail() {
	echo "FAIL: $*" >&2
	for s in A B; do
		echo "--- screen $s:" >&2
		$T capture-pane -p -t $s >&2 || true
	done
	echo "(outputs in $OUT)" >&2
	exit 1
}

# wait_for SESSION FIXED_STRING [TIMEOUT_SECONDS]
wait_for() {
	local deadline=$((SECONDS + ${3:-10}))
	while ! $T capture-pane -p -t "$1" | grep -qF -- "$2"; do
		if [ $SECONDS -ge $deadline ]; then
			fail "timeout waiting for '$2' on $1"
		fi
		sleep 0.2
	done
	echo "ok: $1 shows '$2'"
}

echo "file given on the command line" > "$OUT/src/arg file.txt"
$T new-session -d -s A -x 200 -y 40 "env HOME=$OUT/hA $BIN $ARGS -name e2eA -max-rate 50KB/s '$OUT/src/arg file.txt'"
$T set -g set-clipboard on
$T new-session -d -s B -x 200 -y 40 "env HOME=$OUT/hB $BIN $ARGS -name e2eB -dir $OUT/dlB"

# Discovery both ways before connecting (or the request is dropped as from an unknown peer).
wait_for A "e2eB"
wait_for B "e2eA"
$T send-keys -t A 1
wait_for B "e2eA ("
wait_for B "wants to connect: check the hash, then click or press 1 to accept"
$T send-keys -t B 1
wait_for A "🔗 e2eB"
wait_for B "🔗 e2eA"
# File given as argument to A: offered once B is linked.
wait_for B "e2eA offers $OUT/src/arg file.txt (31 b): click or press d"

# Raw bytes as a terminal would send for a bracketed paste (tmux paste-buffer rewrites control chars).
printf 'hello from A\nsecond\tline \033[31mred\033[0m osc52:\033]52;c;aGFja2Vk\007end\nthird\nfourth\nfifth' > "$OUT/clip.raw"
# shellcheck disable=SC2046 # we want word splitting of the hex bytes.
$T send-keys -t A -H $( (printf '\033[200~'; cat "$OUT/clip.raw"; printf '\033[201~') | od -An -tx1 )
wait_for A "sent to 1 peer"
wait_for B "1 e2eA"
wait_for B "second    line red osc52:end"
wait_for B "… (+3 lines)"

$T send-keys -t B c
wait_for B "Clipboard copied (57 b from e2eA)"
expected=$(printf 'hello from A\nsecond\tline red osc52:end\nthird\nfourth\nfifth')
copied=$($T show-buffer)
if [ "$copied" != "$expected" ]; then
	printf '%s' "$copied" > "$OUT/copied.txt"
	fail "copied clipboard differs from expected (see $OUT/copied.txt)"
fi
if [ "$($T list-buffers | wc -l)" -ne 1 ]; then
	fail "expected exactly 1 tmux buffer (escape injection?): $($T list-buffers)"
fi
echo "ok: clipboard has the sanitized clip"

# Restart B (same identity): it should reconnect on its own (trusted) and get A's last clip again.
$T kill-session -t B
$T new-session -d -s B -x 200 -y 40 "env HOME=$OUT/hB $BIN $ARGS -name e2eB -dir $OUT/dlB"
wait_for B "Auto connecting to trusted peer e2eA" 15
wait_for B "🔗 e2eA"
wait_for A "🔗 e2eB"
wait_for B "second    line red osc52:end"

# Drop a file in A (as GNOME/VTE does: single quoted path + space, in a bracketed paste):
# offered to B, downloaded with `d`, same content; a 2nd download doesn't overwrite.
head -c 300000 /dev/urandom > "$OUT/src/My File.bin"
drop() {
	# shellcheck disable=SC2046 # we want word splitting of the hex bytes.
	$T send-keys -t A -H $( (printf '\033[200~'"'"; printf '%s' "$OUT/src/My File.bin"; printf "' "'\033[201~') | od -An -tx1 )
}
drop
wait_for A "offered $OUT/src/My File.bin (293.0 Kb) to 1 peer"
wait_for B "e2eA offers $OUT/src/My File.bin (293.0 Kb): click or press d"
if [ -n "$(ls -A "$OUT/dlB")" ]; then
	fail "nothing should be written before accepting: $(ls -A "$OUT/dlB")"
fi
$T send-keys -t B d
# A is rate limited (50KB/s, ~6s for this file): check the progress bars, also across resizes.
wait_for B "%"
wait_for A "%"
# no_wider SESSION COLS: fails if a line of the screen is wider than COLS (display columns).
go build -o "$OUT/maxwidth" ./scripts/maxwidth
no_wider() {
	$T capture-pane -p -t "$1" | "$OUT/maxwidth" "$2" || fail "$1 has lines wider than $2 after resize"
}
$T resize-window -t B -x 60 -y 24
$T resize-window -t A -x 60 -y 24
sleep 1
wait_for B "%"
wait_for A "%"
wait_for B "📎 files"
no_wider B 60
no_wider A 60
echo "ok: progress bars and boxes fit after shrinking to 60x24"
$T resize-window -t B -x 200 -y 40
$T resize-window -t A -x 200 -y 40
wait_for B "My File.bin from e2eA saved as" 30
wait_for A "My File.bin sent to e2eB"
cmp "$OUT/src/My File.bin" "$OUT/dlB/My File.bin" || fail "downloaded file differs"
drop
wait_for B "e2eA offers $OUT/src/My File.bin (293.0 Kb): click or press d" # the new offer (not the pending arg file one).
$T send-keys -t B d
wait_for B "My File-1.bin" 30
cmp "$OUT/src/My File.bin" "$OUT/dlB/My File-1.bin" || fail "2nd downloaded file differs"
echo "ok: file dropped, offered, downloaded twice without overwrite"
# Drop a directory (nested, with a symlink that is skipped): offered, downloaded in a new directory, twice.
mkdir -p "$OUT/src/proj/sub/deep"
echo one > "$OUT/src/proj/one.txt"
echo two > "$OUT/src/proj/sub/two.txt"
head -c 100000 /dev/urandom > "$OUT/src/proj/sub/deep/rand.bin"
ln -s one.txt "$OUT/src/proj/link"
# Hidden files, modes (eg executable) and empty directories are kept.
mkdir -p "$OUT/src/proj/.config" "$OUT/src/proj/empty"
echo secret > "$OUT/src/proj/.config/.env"
printf '#!/bin/sh\necho hi\n' > "$OUT/src/proj/run.sh"
chmod 755 "$OUT/src/proj/run.sh"
# shellcheck disable=SC2046 # we want word splitting of the hex bytes.
$T send-keys -t A -H $( (printf '\033[200~'"'"; printf '%s' "$OUT/src/proj"; printf "' "'\033[201~') | od -An -tx1 )
wait_for A "offered $OUT/src/proj (5 files, "
wait_for B "e2eA offers $OUT/src/proj (directory, 5 files, "
$T send-keys -t B d
# A is rate limited: the directory bar and the one of the file in progress.
wait_for B "of sub/deep/rand.bin"
wait_for A "of sub/deep/rand.bin"
wait_for B "proj from e2eA saved as" 30
wait_for A "proj sent to e2eB"
diff -r -x link "$OUT/src/proj" "$OUT/dlB/proj" || fail "downloaded directory differs"
[ ! -e "$OUT/dlB/proj/link" ] || fail "symlink should not be sent"
[ -x "$OUT/dlB/proj/run.sh" ] || fail "run.sh should still be executable"
[ -d "$OUT/dlB/proj/empty" ] || fail "empty directory should be created"
# shellcheck disable=SC2046 # we want word splitting of the hex bytes.
$T send-keys -t A -H $( (printf '\033[200~'"'"; printf '%s' "$OUT/src/proj"; printf "' "'\033[201~') | od -An -tx1 )
wait_for B "e2eA offers $OUT/src/proj (directory, 5 files, " # the new offer (not the pending arg file one).
$T send-keys -t B d
wait_for B "proj-1 from e2eA saved as" 30
diff -r -x link "$OUT/src/proj" "$OUT/dlB/proj-1" || fail "2nd downloaded directory differs"
echo "ok: directory dropped, offered, downloaded twice into new directories"
$T capture-pane -p -t A > "$OUT/A.txt"
$T capture-pane -p -t B > "$OUT/B.txt"
echo "PASS (screens in $OUT)"
