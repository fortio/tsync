[![GoDoc](https://godoc.org/fortio.org/tsync?status.svg)](https://pkg.go.dev/fortio.org/tsync)
[![CI Checks](https://github.com/fortio/tsync/actions/workflows/include.yml/badge.svg)](https://github.com/fortio/tsync/actions/workflows/include.yml)
[![codecov](https://codecov.io/github/fortio/tsync/graph/badge.svg?token=Yx6QaeQr1b)](https://codecov.io/github/fortio/tsync)

# tsync

Share your clipboard and files between your machines (macOS, Linux, Windows) from a terminal UI.

Run `tsync` on each machine of your local network and they find each other. You approve a new machine once, after checking its short hash matches, and from then on they reconnect by themselves over an encrypted link. Paste text in tsync on one machine and click to copy it on another; drop a file in and click to download it on the other side.

The `tsnet` and `tcrypto` packages can also be used as a Go library for local network discovery, authenticated and encrypted links between peers, and clipboard/file transfer.

Clipboard sharing and the file sharing works and is tested between macOS, Windows and Linux machines, including VMs.

![Example Screenshot](screenshot.png)

*tsync on a Mac linked with a Windows PC, a Linux VM and a Linux server: shared clipboard text, a file offered by the Windows machine, one downloaded from the VM and one offered to all.*

## Install

Get the binary from [releases](https://github.com/fortio/tsync/releases), or

```
brew install fortio/tap/tsync
```

or with Go
```
CGO_ENABLED=0 go install fortio.org/tsync@latest  # installs in ~/go/bin typically
CGO_ENABLED=0 go run fortio.org/tsync@latest      # to run without installing
```

Use the same version on all your machines: the protocol still changes, and peers running an older one show as "(old version)".

## Usage

Run `tsync` on each machine. The table lists the peers found, with this machine on the 🏠 line.

- Click on a peer, or press its number, to connect to it. It turns yellow, and on the other machine it blinks blue, with a bell and a "wants to connect" message.
- On the other machine, check that the hash shown is the same as on the first machine's 🏠 line, then click it (or press its number) to accept. Both turn green, then get a 🔗 once the encrypted link is up.
- Accepted peers are remembered and reconnect by themselves when either side restarts or after a network glitch.
- Paste (Cmd-V, Ctrl-Shift-V...) in tsync to send that text to all the linked peers. It shows in a box below the table. Peers connecting later get the last text too.
- Click on a box, or press `c` for the latest one received, to copy it to your clipboard.
- Drag and drop a file in tsync to offer it to the linked peers (terminals paste the dropped file's path, which tsync recognizes, quoted or not). Only its name and size are sent until someone downloads it, and peers connecting later see the offer too.
- On the other machines, click the offer in the files box, or press `d` for the latest, to download it. Nothing is written before that. Files go to `~/Downloads` (or the current directory if there is none, or `-dir`) and never overwrite anything: you get `name-1.ext`, `name-2.ext`, etc.
- `tsync file1 file2...` starts as usual and offers these files, useful over ssh where you can't drag and drop the remote files. If tsync was already running in another window of that machine, that one exits and the new one takes over.
- `q` or Ctrl-C to quit.

The log scrolls below; use `-loglevel debug` for more details.

### Copying to the clipboard

Copying uses the OSC 52 escape sequence, which the terminal has to support and allow. Ghostty, kitty, WezTerm and Windows Terminal work as is. iTerm2 needs "Applications in terminal may access clipboard" turned on, and tmux needs `set -g set-clipboard on`. Apple's Terminal doesn't support it.

Escape sequences and control characters are removed from the text received before it's displayed or copied.

### Flags

```
tsync help
tsync [flags] [file...]
```

The useful ones:
- `-name` to use another name than the hostname, eg to run 2 instances on one machine.
- `-dir` for where received files are saved. Default is `~/Downloads` if it exists, else the current directory.
- `-peers host[:port],...` to also send discovery directly to these addresses when multicast doesn't work at all (some VPNs, cloud VMs...). The port defaults to 29556.
- `-auto-connect=false` to not reconnect automatically to accepted peers.
- `-clip-lines` for how many lines each clipboard box shows (3 by default).
- `-port` and `-mcast` for a different discovery port or multicast group, eg to keep separate groups of machines.

### Trying it on a single machine

Run 2 instances with different names and identities: `tsync -name a` and `HOME=/tmp/b tsync -name b`.

### Troubleshooting

- A peer isn't listed: multicast has to work between the machines. If it only works one way (eg a VM with NAT/"shared" networking, seen by the host but not the reverse), tsync's direct discovery to the peers it sees takes care of it. If it doesn't work at all, use `-peers`.
- A peer stays yellow: the other side hasn't accepted yet, or didn't get the request. tsync resends it a few times; click again to retry later.
- Green but no 🔗: the link uses TCP on a random port, so allow tsync in your firewall (macOS asks on first run; on Windows allow private networks). Through NAT, the side that can't be reached asks the other side to connect instead.
- "(old version)" next to a peer: upgrade tsync there.
- Forgetting a peer: accepted peers are listed, one per line, in `~/.tsync/checked.pub` (`%USERPROFILE%\.tsync\checked.pub` on Windows). Remove the line, or the file, and restart tsync. Leave `id` and `id.pub` alone: they are this machine's identity, and a new one means a new hash that all the peers need to accept again.

## How does it work?

- Interface: tsync first finds which interface and local address to use by checking the route to `-target` (UDP 8.8.8.8:53 by default, one of Google's DNS servers; nothing is sent to it). This avoids picking the WSL virtual interface on Windows for instance.
- Discovery: about every 1.5s, plus some random jitter, each instance sends its name, public key and a random id for this run to a multicast group (239.255.116.115:29556 by default), and also directly to the peers it knows and to `-peers`. Peers not heard from for 10s are removed.
- Identity: each machine has an Ed25519 key pair in `~/.tsync/`, created on first run. The `DDD-DDDD` hash comes from the public key; checking it on both sides is what makes the first connection safe (trust on first use).
- Handshake: connection requests and accepts carry a new X25519 key signed with the sender's identity and tied to both run ids, so old messages can't be replayed. Both sides derive the session keys with HKDF-SHA256, one AES-256-GCM key per direction.
- Link: one side then opens a TCP connection. Every frame on it is encrypted and authenticated, with counter nonces so frames can't be replayed or reordered, and the first frame proves which peer it is.
- Clipboard: the text (1 MiB max) and the time it was copied go to all the linked peers, and the receiver keeps the newest.
- Files: the offer (path, size, modification time) goes to the linked peers. Once accepted, the sender checks the file didn't change and streams it, followed by its SHA-256. The receiver writes a temporary file, checks size and hash, then renames it to the last part of the sender's path, cleaned up: no `../`, control characters, characters Windows refuses or leading dot; look-alike spaces (like the one macOS puts before AM/PM in screenshot names) become normal spaces and invisible characters (eg bidi overrides) are removed, while accents, other scripts and emojis are kept. Saved files are never executable (mode 0644) and keep their modification time.

## Docker

```
docker run --network host -v ~/.tsync:/home/user/.tsync -ti fortio/tsync
```
but the multicast discovery doesn't seem to work, at least on Docker for Mac.

## Development

```
make test   # unit and (local network multicast) integration tests
make lint   # golangci-lint with the fortio shared config
make e2e    # end to end TUI test with 2 instances in tmux: connect, paste, copy, restart, drop and download a file (needs tmux)
```
