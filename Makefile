BIN     := tmx
COMP    := completion/_tmx

# Personal settings (PEERS, a different ZSH_COMPDIR) go in local.mk, which
# git ignores. It is read before the defaults below so that its values win.
-include local.mk

PREFIX      ?= $(HOME)/.local
BINDIR      ?= $(PREFIX)/bin
# Where zsh looks for completion functions; it must be on fpath before
# compinit runs.
ZSH_COMPDIR ?= $(PREFIX)/share/zsh/site-functions
# The ssh hosts `make deploy` and `make status` copy to and ask. Empty by
# default: set it in local.mk or on the command line.
PEERS       ?=

# On a peer, the binary and completion go to the same places relative to
# its home. A folder outside this machine's home is used as written.
REMOTE_BINDIR  := $(patsubst $(HOME)/%,%,$(BINDIR))
REMOTE_COMPDIR := $(patsubst $(HOME)/%,%,$(ZSH_COMPDIR))

# The commit the binary was built from, so `tmx --version` and
# `make status` show a stale copy. Outside git it stays at the source's dev.
VERSION := $(or $(shell git describe --always --dirty 2>/dev/null),dev)

.PHONY: test build install deploy status clean need-peers

test:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -race ./...

build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o $(BIN) .

# Copy then rename, so a tmx already running keeps its old file intact.
# ~/.config/tmx.conf is each machine's own and is never touched here.
install: build
	mkdir -p '$(BINDIR)' '$(ZSH_COMPDIR)'
	cp $(BIN) '$(BINDIR)/.$(BIN).new'
	mv -f '$(BINDIR)/.$(BIN).new' '$(BINDIR)/$(BIN)'
	cp $(COMP) '$(ZSH_COMPDIR)/._tmx.new'
	mv -f '$(ZSH_COMPDIR)/._tmx.new' '$(ZSH_COMPDIR)/_tmx'

need-peers:
	@test -n "$(strip $(PEERS))" || { echo 'set PEERS, in local.mk or as make deploy PEERS="server1 devbox"' >&2; exit 1; }

# The same copy-then-rename on each peer, over ssh. Their tmx.conf stays
# theirs. ssh and scp start in the peer's home, so relative paths land
# under it.
deploy: need-peers build
	for p in $(PEERS); do \
	  ssh $$p "mkdir -p '$(REMOTE_BINDIR)' '$(REMOTE_COMPDIR)'" && \
	  scp $(BIN) "$$p:$(REMOTE_BINDIR)/.$(BIN).new" && \
	  scp $(COMP) "$$p:$(REMOTE_COMPDIR)/._tmx.new" && \
	  ssh $$p "mv -f '$(REMOTE_BINDIR)/.$(BIN).new' '$(REMOTE_BINDIR)/$(BIN)' && mv -f '$(REMOTE_COMPDIR)/._tmx.new' '$(REMOTE_COMPDIR)/_tmx'" || exit 1; \
	done

# The installed tmx here and on each peer, always one line per host: its
# version, "not installed", or (for a peer) "unreachable". One bad host
# never stops the rest.
status:
	@h=$$(hostname -s); \
	if [ -x "$(BINDIR)/$(BIN)" ]; then v=$$("$(BINDIR)/$(BIN)" --version 2>&1 | head -n 1); else v='not installed'; fi; \
	printf '%s: %s\n' "$$h" "$$v"
	@for p in $(PEERS); do \
	  v=$$(ssh -o ConnectTimeout=2 -o BatchMode=yes $$p "f='$(REMOTE_BINDIR)/$(BIN)'; "'if [ -x "$$f" ]; then "$$f" --version 2>&1 | head -n 1; else echo "not installed"; fi' 2>/dev/null) || v=unreachable; \
	  printf '%s: %s\n' "$$p" "$${v:-unreachable}"; \
	done

clean:
	rm -f $(BIN)
