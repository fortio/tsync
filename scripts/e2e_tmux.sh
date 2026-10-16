#!/bin/bash
# End to end test of the TUI: runs 2 tsync instances (separate identities) in tmux,
# connects/accepts, pastes (raw bracketed paste with escape sequences) in A, checks
# B shows the sanitized clip and that `c` on B copies it (OSC 52 -> tmux buffer).
# Needs tmux and multicast on the local interface.
# Usage: scripts/e2e_tmux.sh [outdir] (default: a new temp dir, kept on failure)
set -euo pipefail

OUT=${1:-$(mktemp -d -t tsync-e2e)}
mkdir -p "$OUT/hA" "$OUT/hB"
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

$T new-session -d -s A -x 100 -y 30 "env HOME=$OUT/hA $BIN $ARGS -name e2eA"
$T set -g set-clipboard on
$T new-session -d -s B -x 100 -y 30 "env HOME=$OUT/hB $BIN $ARGS -name e2eB"

# Discovery both ways before connecting (or the request is dropped as from an unknown peer).
wait_for A "e2eB"
wait_for B "e2eA"
$T send-keys -t A 1
wait_for B "select it to accept"
$T send-keys -t B 1
wait_for A "🔗 e2eB"
wait_for B "🔗 e2eA"

# Raw bytes as a terminal would send for a bracketed paste (tmux paste-buffer rewrites control chars).
printf 'hello from A\nsecond\tline \033[31mred\033[0m osc52:\033]52;c;aGFja2Vk\007end\nthird\nfourth\nfifth' > "$OUT/clip.raw"
# shellcheck disable=SC2046 # we want word splitting of the hex bytes.
$T send-keys -t A -H $( (printf '\033[200~'; cat "$OUT/clip.raw"; printf '\033[201~') | od -An -tx1 )
wait_for A "sent to 1 peer"
wait_for B "1 e2eA"
wait_for B "second    line red osc52:end"
wait_for B "… (+3 lines)"

$T send-keys -t B c
wait_for B "Copied 57 bytes from e2eA to the clipboard"
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
$T capture-pane -p -t A > "$OUT/A.txt"
$T capture-pane -p -t B > "$OUT/B.txt"
echo "PASS (screens in $OUT)"
