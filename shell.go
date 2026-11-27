package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Non-interactive SSH has no Homebrew PATH, so every script finds tmux by
// full path before falling back to whatever PATH offers.
const findTmux = `t=tmux; for p in /opt/homebrew/bin/tmux /usr/local/bin/tmux; do [ -x "$p" ] && { t=$p; break; }; done`

// A tmux that can't say its version (missing, or an empty file) is no tmux.
const checkTmux = `case $("$t" -V 2>/dev/null) in tmux*) ;; *) exit 3 ;; esac`

// localTmux is the same search as findTmux, for commands run from Go.
func localTmux() string {
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux"} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return "tmux"
}

// q always single-quotes, for text placed inside a script that a shell
// will read, so no remote or user text is ever parsed as code.
func q(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// viaSh wraps a script so a remote login shell hands it to sh whole. Real
// ssh runs its command string through that shell, whatever it is, and zsh
// for one aborts a script on an unmatched glob that sh would leave alone.
func viaSh(script string) string {
	return "sh -c " + q(script)
}

// qMin quotes only when needed, so dry-run output stays readable.
func qMin(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("@%+=:,./_-", r):
		default:
			return q(s)
		}
	}
	return s
}

func quoteArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = qMin(a)
	}
	return strings.Join(parts, " ")
}

// tmuxPrefix cuts $TMUX down to its socket path: tmux appends ",pid,index".
// Without both commas it is left whole, as zsh's ${TMUX%,*,*} would.
func tmuxPrefix(v string) string {
	i := strings.LastIndexByte(v, ',')
	if i < 0 {
		return v
	}
	j := strings.LastIndexByte(v[:i], ',')
	if j < 0 {
		return v
	}
	return v[:j]
}

// resolve makes a path absolute with symlinks followed, like zsh's :A, so
// /tmp and /private/tmp compare equal on macOS. A missing tail is kept as
// written under its resolved parent.
func resolve(p string) string {
	if p == "" {
		return p
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, base := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if dir == abs {
		return abs
	}
	return filepath.Join(resolve(dir), base)
}

type jobResult struct {
	out, err []byte
	rc       int
	timedOut bool
}

// runJob runs argv in its own process group with stdin from /dev/null. If
// ctx ends first the whole group is killed, so no ProxyCommand or tmux
// client it started is left behind.
func runJob(ctx context.Context, argv []string) jobResult {
	var out, errb bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// A grandchild that escaped the group could hold the pipes open; don't
	// wait on it forever.
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return jobResult{err: []byte(err.Error()), rc: 127}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	pgid := cmd.Process.Pid
	var werr error
	timedOut := false
	select {
	case werr = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		werr = <-done
		// A job that had already exited by itself, and was only waiting
		// on a child's pipe, did not time out.
		timedOut = cmd.ProcessState == nil || !cmd.ProcessState.Exited()
	}
	// Whatever the job started and left running (a ProxyCommand, a
	// backgrounded child) must not outlive it, so the group dies after
	// every job, however it ended.
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	rc := 0
	var ee *exec.ExitError
	switch {
	case errors.As(werr, &ee):
		rc = ee.ExitCode()
	case errors.Is(werr, exec.ErrWaitDelay):
		// The job itself exited 0 and a child kept stdout open past the
		// delay. What it printed before that is good; keep it.
	case werr != nil:
		rc = 1
	}
	return jobResult{out: out.Bytes(), err: errb.Bytes(), rc: rc, timedOut: timedOut}
}

// finish runs the final command in place of tmx, or with dry run on only
// prints it, shell-quoted on one line. Once the exec works, the exit code is
// the command's own; tmx only answers for failing to start it, with 1 like
// any other failure.
func (a *app) finish(argv []string) int {
	if a.dryRun {
		fmt.Fprintln(a.stdout, quoteArgv(argv))
		return 0
	}
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(a.stderr, "tmx: %v\n", err)
		return 1
	}
	err = syscall.Exec(path, argv, os.Environ())
	fmt.Fprintf(a.stderr, "tmx: %s: %v\n", argv[0], err)
	return 1
}
