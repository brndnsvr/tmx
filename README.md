# tmx

tmx is one picker for every tmux session on this machine and on your ssh
hosts. Run `tmx`, pick a row, and you are attached, wherever the session
lives. `tmx NAME` creates or rejoins a named session, and starts a new one
in a folder guessed from its name.

- Each host has two kinds of session, and the picker lists both:
  - the plain `default` server, what `tmux` alone gives you;
  - the `tmx` server (`tmux -L tmx`), where `tmx NAME` makes named sessions.
- Attaching never detaches anyone else. The picker never creates or kills
  a session.
- All tmux options live in `~/.tmux.conf`, not here.

## Requirements

- Go 1.27 or later, to build.
- macOS or Linux.
- tmux on each host you want to see.
- For every host other than this one: an entry in `~/.ssh/config` and key
  auth (tmx never asks for a password). See
  [A host needs ssh config and key auth](#a-host-needs-ssh-config-and-key-auth).
- Optional: [fzf](https://github.com/junegunn/fzf) for the picker menu.
  Without it you get a numbered list.

## Install

From a clone:

```
make install
```

That puts the binary in `~/.local/bin/tmx` and the zsh completion in
`~/.local/share/zsh/site-functions/_tmx`. Change either with `PREFIX`
(binary in `$(PREFIX)/bin`) or `ZSH_COMPDIR`, on the command line or in
`local.mk` (see [Deploying to other machines](#deploying-to-other-machines)).

Or with Go alone (no completion, and `tmx --version` says `tmx dev`):

```
go install github.com/brndnsvr/tmx@latest
```

Make sure the binary's folder is on your `PATH`. For the completion, add
its folder to `fpath` in `~/.zshrc` **before** `compinit` runs:

```
fpath=(~/.local/share/zsh/site-functions $fpath)
autoload -Uz compinit && compinit
```

Then write `~/.config/tmx.conf` (start from `tmx.conf.example`) and run
`tmx doctor`.

## Commands

| Command | What it does |
| --- | --- |
| `tmx` | Pick a session on this machine or any configured host. |
| `tmx -l` / `--local` | Pick a session on this machine only. |
| `tmx -r server1` / `--remote server1` | Pick a session on server1 only. |
| `tmx webapp` | Create or rejoin `webapp` on this machine. |
| `tmx -r server1 api-2` | Create or rejoin `api-2` on server1, over `ssh -t`. |
| `tmx --dir ~/scratch notes` | Create `notes` in `~/scratch` (if it is new). |
| `tmx doctor` | Check every host: ssh config, key auth, tmux, servers. |
| `tmx --hosts` | Print this machine, then the configured hosts, one per line. |
| `tmx --version` | Print `tmx <git describe>`, or `tmx dev` for an unstamped build. |
| `tmx -h` | Short usage. |

`doctor` is a command, so a session called doctor needs `tmx -- doctor`.

### Environment

- `TMX_CONF` and `TMX_SSH_CONFIG` replace the paths of
  `~/.config/tmx.conf` and `~/.ssh/config`.
- `TMX_DRYRUN=1 tmx ...` prints the final command instead of running it.
- `TMX_SSH=/path/to/ssh` swaps the ssh binary.

### Exit codes

- `0` done.
- `1` failed, or nothing was picked (Esc, EOF, a bad number, a status row).
  Also `tmx -r HOST` for a host that `~/.ssh/config` doesn't have, and
  `tmx doctor` when any host has a problem.
- `2` usage error: a bad flag, a config that doesn't parse, or a host that
  is not in `~/.config/tmx.conf`.

Once tmx hands over to tmux or ssh, the exit code is theirs.

## The config file

`~/.config/tmx.conf` lists the hosts *this* machine can reach. Each
machine has its own file. With no file, tmx sees only this machine and
says nothing about it; `tmx doctor` says so. `tmx.conf.example` is a
commented starting point.

This machine is always listed first and never needs a line. Its name is
its hostname cut at the first dot (`laptop.local` is `laptop`).

The format is line based. `#` at the start of a word begins a comment;
blank lines are ignored. Names and paths hold no spaces. Indented lines
belong to the `host` line above them; any top-level line ends that block.

### Top-level keys

**`servers NAME...`**, once: the tmux servers listed on every host
(`tmux -L NAME`). Optional; the default is:

```
servers default tmx
```

**`strip-prefix P...`**, once: prefixes a session name loses on the way to
its base name. The first one in the list that matches comes off, and only
one. Optional; the default is none.

**`strip-suffix S...`**, once: suffixes the base name loses, as many times
and in any order. Optional; the default is none. A trailing `-<digits>`
(a counter or a date) always comes off, with or without this line.

```
strip-prefix wip-
strip-suffix -run -test
```

With those lines, `wip-webapp-run`, `webapp-test-2` and `webapp-20260301`
all have the base name `webapp`. Without them, only the last one does.
See [Where a new session starts](#where-a-new-session-starts) for what the
base name is used for.

**`host NAME`**: an ssh alias. The order in the file is the picker order,
after this machine.

```
host server1
host devbox
host buildbox
```

A `host` line naming this machine (in any case, since a hostname may be
capitalised) adds no second row, but its indented settings apply to
sessions started here.

### Host keys (indented)

**`ignore-size yes|no`**, default `yes`. Picker attaches use
`tmux attach -f ignore-size` so they never resize a window someone else is
using. A host that runs `window-size latest` wants `no`: there the flag
leaves the window sized for the other client and fills the rest with dots.

**`folder KEYWORD PATH`**, repeatable. A new session on that host whose
base name holds KEYWORD starts in PATH, relative to that host's home.

**`repos DIR...`**, once. Where to look for git repos on that host,
relative to its home, each searched up to 4 levels deep. The default is
`repos code` (so `~/code`). Home's direct children (`~/*/.git`) are always
checked as well. A folder that doesn't exist is skipped.

```
host buildbox
  ignore-size no
  folder api code/webapp/api
  folder docs work/docs-site
  repos src work
```

### Mistakes

A mistake stops every command with the file, the line and what is wrong,
and exit 2:

```
tmx: ~/.config/tmx.conf:7: ignore-size takes yes or no
```

That covers an unknown keyword, a bad value, a key given twice, a path
that starts with `/` or `~` (paths are relative to home), an indented line
with no `host` above it, and a host listed twice. ssh aliases are case
sensitive, but two `host` lines that differ only in case are refused too,
since they are nearly always one machine listed twice. A byte order mark
at the start of the file is ignored. `tmx -h` and `tmx --version` still
work while the file is broken.

## A host needs ssh config and key auth

tmx never prompts and never tries a host ssh doesn't know.

- A host counts as known when ssh would apply a `Host` stanza to its name
  other than a catch-all. tmx reads `~/.ssh/config` in order, the way ssh
  does:
  - Patterns are matched against the name as written in `tmx.conf`, and
    are case sensitive, as in ssh: `Host Server1` does not apply to
    `server1`. `*` and `?` work, and a `!` pattern on the line rules the
    name out.
  - A catch-all (a pattern of only `*`, so `*` or `**`) never counts,
    since it holds defaults for every host. It still starts a block, so an
    `Include` under `Host *` is read.
  - An `Include` is followed when it comes before any `Host` line or sits
    in a block that applies to the name, and ignored in one that doesn't,
    as ssh does. Paths are relative to `~/.ssh`, `~` is home, and globs
    expand in sorted order; as in ssh, `*` skips names that start with a
    dot.
  - **`Match` blocks are not evaluated.** tmx can't tell whether `Match
    exec ...` or `Match user ...` would apply, so it treats every `Match`
    block as one that doesn't. A `Host` line reached only through an
    `Include` inside a `Match` block is not counted. If a host shows `not
    in ~/.ssh/config` but ssh knows it, move its `Host` line out of the
    `Match` block.
- A configured host that ssh doesn't know shows the status row
  `not in ~/.ssh/config` in the picker, and ssh is never started for it.
  `tmx -r HOST` stops with that reason and exit 1. If `~/.ssh/config`
  can't be read at all, every host says so instead.
- Every other host is listed with `BatchMode=yes` and password and
  keyboard-interactive auth off. A host without key auth shows
  `auth refused` instead of asking for a password.

### tmx doctor

`tmx doctor` checks every host at once (5 seconds each, in parallel) and
prints one line per host: this machine first, then the config's hosts.

```
config: ~/.config/tmx.conf
laptop    ok  tmux 3.5a, servers: default tmx
server1   ok  tmux 3.4, servers: tmx
devbox    not in ~/.ssh/config
buildbox  auth refused
```

For each host it checks, in order: a `Host` entry in `~/.ssh/config`, key
auth (one `ssh -o BatchMode=yes` trip with the same options as the
listing), tmux there, and which of the configured servers are running. A
host running none of them is fine (`servers: none`). It exits 0 when every
host is ok, 1 when any host has a problem.

### Adding a host

1. Add a `host NAME` line to `~/.config/tmx.conf` on each machine that
   should see it, with any settings indented under it.
2. Give `~/.ssh/config` on those machines a `Host` entry for NAME that
   logs in with a key, so `ssh -o BatchMode=yes NAME true` works.
3. Run `tmx doctor`. The host's line should say `ok`.

## The picker

`tmx` lists every session on every configured server of every host, one
row each, and status rows for hosts that list nothing (`no sessions`,
`no tmux`, `unreachable`, `timed out`, ...). The listing waits about 3
seconds in all, so a dead host costs no more than that.

The picker uses fzf when you are at a terminal and fzf is installed.
Otherwise it prints a numbered list on stderr and reads a number.

Inside tmux:

- In another tmux server, tmx warns and nests (`env -u TMUX`). Press the
  prefix twice to reach the inner session.
- Already a client of the right server, it moves this client there with
  `switch-client` instead of nesting.

## Named sessions

`tmx NAME` creates or rejoins NAME on this machine's `tmx` server;
`tmx -r HOST NAME` does the same on HOST over `ssh -t`. An existing
session is joined as it is. A new one starts in `--dir DIR` if given, or
in a folder tmx picks (next section).

### Names with `:` or `.`

tmux creates a session called `c:d` or `a.b` without complaint, but
`new-session -A -s c:d` and `attach -t c:d` read that name as
`session:window.pane` and fail with `can't find window: d`. So tmx never
asks tmux to attach by name. Its start script lists the server's
sessions, looks for the exact name and attaches by session id. Only when
there is no such session does it run `new-session -s NAME`, which takes
the name as given. Spaces, quotes, `$` and leading or trailing spaces in a
name are quoted for `sh` and survive.

## Where a new session starts

Only a **new** session gets a folder. An existing session is joined as it
is, with no `-c`. tmx still makes the one trip to find that out, but it
skips the repo search, since nothing would use it.

`--dir DIR` always wins and skips the lookup. A leading `~/` (or a lone
`~`) is expanded, so `--dir=~/scratch` works even though the shell leaves
that tilde alone. For `-r HOST`, the `~` means HOST's home and is expanded
there. `~user` is not expanded.

Otherwise tmx makes one trip to the host (ssh for another host, plain
`sh -c` for this one) to read the `tmx` server's sessions and the host's
git repos (the host's `repos` folders, `~/code` by default, up to 4 levels
deep, plus `~/*/.git`). Then it takes the first rule that fits the
session's **base name**: the name less one `strip-prefix`, then any run of
`strip-suffix` or `-<digits>` endings.

The examples use this config and these folders:

```
strip-suffix -run
host server1
  folder docs work/docs-site
```

```
~/code/webapp/.git
~/code/infra/.git
~/api/.git
```

1. **Folder.** A `folder` keyword from that host's config is in the base
   name; the longest keyword wins. `tmx -r server1 docs-review` starts in
   `~/work/docs-site` (folder map docs). For sessions on this machine, the
   folders come from a `host` line that names this machine.
2. **Sibling.** Another session with the same base name; its active pane's
   folder, the most recently used one if there are several. With
   `webapp-001` running in `~/code/webapp/frontend`, `tmx webapp-run`
   starts there too (sibling webapp-001).
3. **Repo, exact.** A repo folder named exactly like the base name; the
   shortest path wins. `tmx infra-20260301` starts in `~/code/infra`
   (repo).
4. **Repo inside the name.** A repo whose name is a whole run of the base
   name's `-`/`_` parts; the longest repo name wins. `tmx api-load-test`
   starts in `~/api` (repo api). A repo called `ops` matches `ops-tools`,
   never `devops`.
5. **Fallback.** On this machine: the folder you ran tmx from (here), or
   home if that folder is unknown. On another host: home.

Repos that look like copies or leftovers never count. This is a general
rule, not a setting: any path holding `_archive`, `.trash`,
`node_modules` or `.worktrees`, or a folder whose name starts with `OLD_`.

tmx prints one line on stderr when it makes a new session:

```
tmx: new session infra-20260301 in ~/code/infra (repo)
```

Where the chosen folder is gone:

- On this machine, tmx checks the folder first and uses the fallback
  instead. The note says so: `here; chosen folder missing`.
- On another host, tmx can't check, and the note still names the folder it
  chose. Only the start script, running there, falls back to home when the
  folder is not there.

If the lookup fails on this machine, tmx still starts the session here and
says why (`tmx: folder lookup: timed out; a new session starts here`). On
another host a failed lookup is an error (exit 1): `tmx: server1: unreachable`.

## Deploying to other machines

You can build once and copy tmx to your other machines over ssh:

```
make deploy PEERS="server1 devbox"
make status PEERS="server1 devbox"
```

- `deploy` builds, then copies the binary and completion to each peer, to
  the same places relative to its home as `make install` uses here
  (`~/.local/bin/tmx`, `~/.local/share/zsh/site-functions/_tmx` by
  default). It copies to a temp name and renames, so a running tmx is
  never cut short. With no `PEERS` it says `set PEERS` and stops.
- `status` prints `tmx --version` here and on each peer, one line per
  host (`not installed` or `unreachable` where it applies). Every line
  should show the same version. A `-dirty` version was built from
  uncommitted changes.
- Neither touches `~/.config/tmx.conf`; that file is each machine's own.

Settings you always use go in `local.mk` next to the Makefile. git
ignores it and the Makefile reads it first, so its values win:

```
PEERS       := server1 devbox
ZSH_COMPDIR := $(HOME)/.zsh/completions
```

## Tests

```
make test      # gofmt check, go vet, then go test -race
make build     # ./tmx, stamped with git describe of this repo
```

The tests:

- run tmx in-process with dry run on, and check the command it would run;
- read hosts from a temp `TMX_CONF` and `TMX_SSH_CONFIG`, never the real
  files;
- use only throwaway tmux servers on `-L tmxtest-<pid>` sockets, killed at
  the end, never `tmx` or `default`;
- send every "remote" host through a fake ssh script that runs the command
  locally or fails the way ssh does, and records each host it is asked
  for, so nothing touches the network and a test can prove ssh never ran;
- point `HOME` at temp folders, so a lookup never reads the real home;
- keep sessions named `c:d`, `a.b` and one with quotes, `$` and spaces on
  their own throwaway server, and run the start script for real against it
  (only its final attach is swapped, since there is no terminal), outside
  tmux, nested and through the fake ssh.

The tests need tmux and zsh installed, and run on macOS.

## Code map

- `config.go`: the `tmx.conf` parser, the `Host` type and host order.
- `sshconfig.go`: reads `~/.ssh/config` with its includes and answers
  "does ssh know this host?".
- `doctor.go`: `tmx doctor`.
- `picker.go`: listing, the menu, attaching.
- `named.go`, `folder.go`: `tmx NAME` and the folder rules.
- `shell.go`: quoting, jobs with timeouts, the final exec.
- `term_darwin.go`, `term_linux.go`: "is stdin a terminal?" per OS.

The base-name and repo rules are `baseName`, `skipRepo` and `pickFolder`
in `folder.go`, with their table tests in `unit_test.go`.

## Why it is built this way

- **One config file per machine.** Which hosts a machine can reach depends
  on its network (a VM may see only its neighbours), so each machine says
  what it sees instead of one shared table guessing for all of them.
- **ssh config required.** Without a `Host` entry, ssh would fall back to
  DNS and default keys, which on a laptop off its home network can mean a
  long wait or the wrong machine. Requiring the entry keeps the listing
  fast and predictable.
- **`sh -c` around every remote script.** ssh runs its command through the
  remote login shell, whatever that is. zsh, for one, aborts a script on an
  unmatched glob (`~/*/.git` on a host with no such repo) where sh carries
  on. So each script goes over as one `sh -c '...'` argument.
- **Process groups for timeouts.** Each listing job runs in its own process
  group, and the whole group is killed when the job ends or its deadline
  passes (3 seconds for a listing, 5 for doctor, 10 for a folder lookup).
  That takes any ProxyCommand or stray tmux client with it, so a dead host
  never leaves processes behind.
- **ControlMaster=no on listings.** A listing job may be killed at the
  deadline. A killed ssh must never leave a ControlPersist master behind,
  so listing and doctor jobs never start one. The final attach uses the
  normal ssh config, mux masters included.
- **Attach by session id, checked again first.** `-t NAME` splits a name at
  `:`, and a session can be renamed or replaced between the list and the
  pick. The attach script checks the id still carries the listed name.
- **Full path to tmux.** Non-interactive ssh may not have the PATH your
  login shell sets (Homebrew's, for one), so the scripts try
  `/opt/homebrew/bin/tmux` and `/usr/local/bin/tmux` first, then `tmux`
  on PATH.
