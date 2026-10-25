all: lint test

test:
	go test -race ./...

lint: .golangci.yml
	golangci-lint run

.golangci.yml: Makefile
	curl -fsS -o .golangci.yml https://raw.githubusercontent.com/fortio/workflows/main/golangci.yml

# End to end TUI test, needs tmux (and local multicast so not run in CI).
e2e:
	./scripts/e2e_tmux.sh
	./scripts/e2e_sync.sh

# Just the 3 instances directory sync end to end test.
e2e-sync:
	./scripts/e2e_sync.sh

.PHONY: all lint test e2e e2e-sync
