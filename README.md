[![GoDoc](https://godoc.org/fortio.org/tsync?status.svg)](https://pkg.go.dev/fortio.org/tsync)
[![Go Report Card](https://goreportcard.com/badge/fortio.org/tsync)](https://goreportcard.com/report/fortio.org/tsync)
[![CI Checks](https://github.com/fortio/tsync/actions/workflows/include.yml/badge.svg)](https://github.com/fortio/tsync/actions/workflows/include.yml)
[![codecov](https://codecov.io/github/fortio/tsync/graph/badge.svg?token=Yx6QaeQr1b)](https://codecov.io/github/fortio/tsync)

# tsync

**WIP** (just started)

Cross platform terminal UI (tui) and network based synchronization of clipboard and files

Includes reusable library for network discovery and file/dir sync.

## What does it do / How does it work?

The program starts by figuring out which interface and local address to use (because on windows the default picks WSL virtual interface and thus fails to see real peers) by looking up a configurable target (defaults to udp 8.8.8.8:53 ie one of google's dns server)

It then does listens to a multi cast address (default 239.255.116.115:29556) and also periodically sends to that address its own information and reads peers found information.

## Install
You can get the binary from [releases](https://github.com/fortio/tsync/releases)

Or just run
```
CGO_ENABLED=0 go install fortio.org/tsync@latest  # to install (in ~/go/bin typically) or just
CGO_ENABLED=0 go run fortio.org/tsync@latest  # to run without install
```

or even
```
docker run -ti fortio/tsync # but that's obviously slower
```

or
```
brew install fortio/tap/tsync
```


## Usage

```
tsync help
```
