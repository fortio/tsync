[![GoDoc](https://godoc.org/fortio.org/tsync?status.svg)](https://pkg.go.dev/fortio.org/tsync)
[![CI Checks](https://github.com/fortio/tsync/actions/workflows/include.yml/badge.svg)](https://github.com/fortio/tsync/actions/workflows/include.yml)
[![codecov](https://codecov.io/github/fortio/tsync/graph/badge.svg?token=Yx6QaeQr1b)](https://codecov.io/github/fortio/tsync)

# tsync

**WIP** (just started)

Cross platform terminal UI (tui) and network based synchronization of clipboard and files

Includes reusable library for network discovery and file/dir sync.

## What does it do / How does it work?

The program starts by figuring out which interface and local address to use (because on Windows the default picks the WSL virtual interface and thus fails to see real peers) by looking up a configurable target (defaults to UDP 8.8.8.8:53, i.e., one of Google's public DNS servers).

It then listens on a multicast address (default 239.255.116.115:29556), periodically sends its own information to that address, and reads information from discovered peers.

Currently, example of peer detection with tsync running on a mac, a linux and a windows box:

![Example Screenshot](screenshot.png)


## Install
You can get the binary from [releases](https://github.com/fortio/tsync/releases)

Or just run
```
CGO_ENABLED=0 go install fortio.org/tsync@latest  # to install (in ~/go/bin typically) or just
CGO_ENABLED=0 go run fortio.org/tsync@latest  # to run without install
```

or
```
brew install fortio/tap/tsync
```

or even - but multicast that we need doesn't seem to work at least on docker for mac.
```
docker run --network host -v ~/.tsync:/home/user/.tsync -ti fortio/tsync
```


## Usage

Run `tsync` on each machine (same LAN). Peers are discovered automatically and listed in the table.

- Click on a peer (or press its number) to connect: it turns yellow, and blinks blue on the other machine, which also gets a bell and a highlighted "wants to connect" line.
- On the other machine, check the hash matches and click (or press its number) to accept: both turn green and get a 🔗 once the encrypted link is up. Accepted peers are remembered (`~/.tsync/checked.pub`) and auto connected/accepted next time. To forget peers, remove their line (or the whole file) and restart tsync (don't remove `id`/`id.pub`, that's this machine's identity).
- Paste (Cmd-V / Ctrl-Shift-V...) into tsync to send that text to all the linked peers. It shows up in a box below the table on the other machines.
- Click on a box (or press `c` for the latest received) to copy it to your clipboard. This uses OSC 52, which your terminal must support/allow (eg iTerm2: "Applications in terminal may access clipboard", tmux: `set -g set-clipboard on`; Apple's Terminal doesn't support it).
- `q` to quit.

To try it on a single machine, run 2 instances with different names and identities, eg `tsync -name a` and `HOME=/tmp/b tsync -name b`.

```
tsync help
```
