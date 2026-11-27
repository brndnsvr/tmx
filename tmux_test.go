package main

// These tests drive tmx in-process with dry run on, so it prints the
// command it would run instead of running it. They list only throwaway
// tmux servers on their own -L tmxtest-<pid> sockets, killed afterwards,
// and every "remote" host goes through a fake ssh that runs the script
// locally or fails the way ssh does. Nothing opens a network connection.
// HOME points at an empty temp folder for the whole run, so a folder lookup
// on this host never reads the real home and its answers never depend on it,
// and TMX_CONF and TMX_SSH_CONFIG point at temp files, so the real config
// is never read either.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const self = "testself"

var (
	pid      = os.Getpid()
	socket   = fmt.Sprintf("tmxtest-%d", pid)       // holds NAME
	empty    = fmt.Sprintf("tmxtest-empty-%d", pid) // never started
	siblings = fmt.Sprintf("tmxtest-sib-%d", pid)   // for the folder pick
	awkward  = fmt.Sprintf("tmxtest-awk-%d", pid)   // sessions with awkward names
	name     = "tmx test: a b"                      // a space and a colon, the awkward cases

	fakeSSH  string
	fakeHome string
	bareHome string // no ~/*/.git at all
	hangPids string
	leakPids string
	sibDir   string
	sshCalls string // every host the fake ssh was asked for, one per line
	// An ssh config naming every fake host, so they all count as known.
	fakeSSHConf sshConfig
)

// Names that tmux takes as they are when it creates a session, but reads
// as a target (session:window.pane) when it is asked to find one, and ones
// a shell would mangle.
var awkwardNames = []string{
	"c:d",
	"a.b",
	`  it's "q" $HOME * x:y.z `,
}

func sockPath(server string) string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), server)
}

// tmuxCmd talks to one throwaway server, never the caller's own.
func tmuxCmd(server string, args ...string) *exec.Cmd {
	cmd := exec.Command(localTmux(), append([]string{"-f", "/dev/null", "-L", server}, args...)...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	return cmd
}

func TestMain(m *testing.M) {
	os.Exit(func() int {
		tmp, err := os.MkdirTemp("", "tmxtest-")
		if err != nil {
			panic(err)
		}
		tmp, _ = filepath.EvalSymlinks(tmp)
		defer os.RemoveAll(tmp)
		for _, s := range []string{socket, siblings, awkward} {
			defer func() {
				_ = tmuxCmd(s, "kill-server").Run()
				// tmux leaves the dead socket file behind.
				_ = os.Remove(sockPath(s))
			}()
		}
		if out, err := tmuxCmd(socket, "new-session", "-d", "-s", name, "sleep 3600").CombinedOutput(); err != nil {
			panic(fmt.Sprintf("start test server: %v %s", err, out))
		}

		fakeHome = filepath.Join(tmp, "home")
		for _, d := range []string{
			"code/team/web/webapp/.git",
			"code/_archive/webapp/.git",
			"code/a/b/c/d/webapp/.git", // deeper than -maxdepth 4
			"api-server/.git",
			"src/infra/.git", // found only where a host's repos name src
			"sib-here",
		} {
			if err := os.MkdirAll(filepath.Join(fakeHome, d), 0o755); err != nil {
				panic(err)
			}
		}
		bareHome = filepath.Join(tmp, "bare")
		if err := os.MkdirAll(filepath.Join(bareHome, "plain-folder"), 0o755); err != nil {
			panic(err)
		}
		os.Setenv("HOME", bareHome)
		os.Setenv("TMX_CONF", filepath.Join(tmp, "no-such-tmx.conf"))
		os.Setenv("TMX_SSH_CONFIG", filepath.Join(tmp, "no-such-ssh-config"))
		sibDir = filepath.Join(fakeHome, "sib-here")
		if out, err := tmuxCmd(siblings, "new-session", "-d", "-s", "api-server-001", "-c", sibDir, "sleep 3600").CombinedOutput(); err != nil {
			panic(fmt.Sprintf("start sibling server: %v %s", err, out))
		}

		for _, n := range awkwardNames {
			if out, err := tmuxCmd(awkward, "new-session", "-d", "-s", n, "sleep 3600").CombinedOutput(); err != nil {
				panic(fmt.Sprintf("start awkward server: %v %s", err, out))
			}
		}
		if out, err := tmuxCmd(awkward, "list-sessions", "-F", "#{session_name}").Output(); err != nil ||
			strings.Count(string(out), "\n") != len(awkwardNames) {
			panic(fmt.Sprintf("tmux changed an awkward name: %q %v", out, err))
		}

		sshCalls = filepath.Join(tmp, "ssh.calls")
		sshConfPath := filepath.Join(tmp, "ssh_config")
		if err := os.WriteFile(sshConfPath, []byte("Host fake*\n  User nobody\n"), 0o600); err != nil {
			panic(err)
		}
		fakeSSHConf, _ = loadSSHConfig(sshConfPath, tmp, tmp)
		hangPids = filepath.Join(tmp, "hang.pids")
		leakPids = filepath.Join(tmp, "leak.pids")
		fakeSSH = filepath.Join(tmp, "ssh")
		script := `#!/bin/sh
host= last=
for a in "$@"; do host=$last; last=$a; done
printf '%s\n' "$host" >> ` + q(sshCalls) + `
case $host in
  fakeok) HOME=` + q(fakeHome) + ` exec /bin/zsh -c "$last" ;;
  fakebare) HOME=` + q(bareHome) + ` exec /bin/zsh -c "$last" ;;
  fakenotmux) last=$(printf '%s\n' "$last" | sed -e 's#/opt/homebrew/bin/tmux#/no/tmux#g' -e 's#/usr/local/bin/tmux#/no/tmux#g')
    PATH=/usr/bin:/bin exec /bin/zsh -c "$last" ;;
  fakeleak) printf '1:1:0:$1:leaky\n'; sleep 5 & echo $! > ` + q(leakPids) + `; exit 0 ;;
  fakekey) echo 'Host key verification failed.' >&2; exit 255 ;;
  fakeauth) echo 'u@fakeauth: Permission denied (publickey).' >&2; exit 255 ;;
  fakedown) echo 'ssh: connect to host fakedown port 22: No route to host' >&2; exit 255 ;;
  fakehang) sleep 30 & echo $$ $! > ` + q(hangPids) + `; wait ;;
  *) echo "fake ssh: unexpected host $host" >&2; exit 255 ;;
esac
`
		if err := os.WriteFile(fakeSSH, []byte(script), 0o755); err != nil {
			panic(err)
		}
		return m.Run()
	}())
}

type run struct {
	rc     int
	stdout string
	stderr string
}

// Real ssh hands its command string to the remote login shell. The fake
// uses zsh, the strictest common one: it aborts a script on an unmatched
// glob, where sh carries on.

// tmx runs the program on test hosts and servers; tweak edits the app
// before it runs.
func tmx(t *testing.T, stdin string, tweak func(*app), args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	a := &app{
		hosts:         []Host{{Name: self, IgnoreSize: true}},
		servers:       []string{socket},
		strip:         testStrip,
		namedServer:   socket,
		self:          self,
		ssh:           fakeSSH,
		dryRun:        true,
		tmuxTmpdir:    os.Getenv("TMUX_TMPDIR"),
		pwd:           "/some/dir",
		stdin:         strings.NewReader(stdin),
		stdout:        &out,
		stderr:        &errb,
		listTimeout:   3 * time.Second,
		gatherTimeout: 5 * time.Second,
		doctorTimeout: 5 * time.Second,
		confShown:     "~/.config/tmx.conf",
		sshConf:       fakeSSHConf,
		sshShown:      "~/.ssh/config",
	}
	if tweak != nil {
		tweak(a)
	}
	rc := a.main(args)
	return run{rc, out.String(), errb.String()}
}

func ok(t *testing.T, r run) {
	t.Helper()
	if r.rc != 0 {
		t.Fatalf("rc %d\nstdout %s\nstderr %s", r.rc, r.stdout, r.stderr)
	}
}

func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("missing %q in:\n%s", sub, s)
		}
	}
}

func mustLack(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Errorf("unexpected %q in:\n%s", sub, s)
		}
	}
}

// What tmx runs on this host for tmx NAME: [env -u TMUX] sh -c SCRIPT.
func localScript(t *testing.T, r run) string {
	t.Helper()
	argv := shellSplit(t, r.stdout)
	if len(argv) > 3 && argv[0] == "env" {
		argv = argv[3:]
	}
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("not sh -c SCRIPT: %q", r.stdout)
	}
	return argv[2]
}

// startsIn checks where a start script creates a new session: no -c at all
// for an empty dir.
func startsIn(t *testing.T, script, dir string) {
	t.Helper()
	if dir == "" {
		mustLack(t, script, " -c ", "; d=")
		return
	}
	mustContain(t, script, "; d="+q(dir)+"; ", `new-session -s "$n" -c "$d"`)
}

// runStart runs the command tmx printed, with only its last step swapped
// for something that works without a terminal: joining a session becomes
// a display-message that prints the joined session's name, and creating
// one gets -d and prints its name and folder. The lookup by name, the
// quoting and the ssh sh -c layer all run as shipped, against the real
// throwaway server.
func runStart(t *testing.T, r run) string {
	t.Helper()
	argv := shellSplit(t, r.stdout)
	last := len(argv) - 1
	remote := argv[0] == fakeSSH
	script := argv[last]
	if remote {
		script = unwrapSh(t, script)
	}
	swap := func(from, to string) {
		if strings.Count(script, from) != 1 {
			t.Fatalf("expected one %q in %q", from, script)
		}
		script = strings.Replace(script, from, to, 1)
	}
	swap(`attach-session -t "$id"`, `display-message -p -t "$id" '#{session_name}'`)
	// pane_start_path is what tmux was told with -c. pane_current_path asks
	// the pane's process, which under load may not have a folder yet.
	swap(`new-session -s "$n"`, `new-session -d -P -F '#{session_name}|#{pane_start_path}' -s "$n"`)
	argv[last] = script
	if remote {
		argv[last] = viaSh(script)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s\nscript: %s", err, out, script)
	}
	return strings.TrimSuffix(string(out), "\n")
}

// Named sessions on this host.

func TestNamedOutsideTmuxCreatesOrRejoins(t *testing.T) {
	r := tmx(t, "", nil, "work")
	ok(t, r)
	s := localScript(t, r)
	mustContain(t, s, "n='work'", "s="+q(socket), `attach-session -t "$id"`)
	startsIn(t, s, "/some/dir")
	mustLack(t, r.stdout, "env -u TMUX", "new-session -A")
}

func TestNamedInsideAnotherServerNestsWithAWarning(t *testing.T) {
	r := tmx(t, "", func(a *app) { a.tmuxEnv = "/nonexistent/tmux-0/default,1,0" }, "work")
	ok(t, r)
	if !strings.HasPrefix(r.stdout, "env -u TMUX sh -c ") {
		t.Errorf("stdout %q", r.stdout)
	}
	mustContain(t, localScript(t, r), "n='work'", `new-session -s "$n"`)
	mustContain(t, r.stderr, "nested in tmux")
}

func TestNamedInsideTheTmxServerSwitchesInsteadOfNesting(t *testing.T) {
	absent := fmt.Sprintf("tmxtest-absent-%d", pid)
	inside := func(a *app) { a.tmuxEnv = sockPath(socket) + ",1,0" }
	r := tmx(t, "", inside, absent)
	ok(t, r)
	mustContain(t, r.stdout, "new-session -d -s "+absent, "switch-client")
	mustLack(t, r.stdout, "new-session -A")
	mustLack(t, r.stderr, "nested")

	// An existing session is joined by id, and nothing is created.
	r = tmx(t, "", inside, name)
	ok(t, r)
	if !regexp.MustCompile(`switch-client -t '\$\d+'\n$`).MatchString(r.stdout) {
		t.Errorf("stdout %q", r.stdout)
	}
	mustLack(t, r.stdout, "new-session")

	// A name with a colon or a dot is found by exact name, then joined by id.
	for _, n := range awkwardNames {
		r = tmx(t, "", func(a *app) { a.namedServer = awkward; a.tmuxEnv = sockPath(awkward) + ",1,0" }, n)
		ok(t, r)
		if !regexp.MustCompile(`switch-client -t '\$\d+'\n$`).MatchString(r.stdout) {
			t.Errorf("%q: stdout %q", n, r.stdout)
		}
		mustLack(t, r.stdout, "new-session")
	}
}

func TestNamedDirFlag(t *testing.T) {
	r := tmx(t, "", nil, "--dir", "/elsewhere", "work")
	ok(t, r)
	startsIn(t, localScript(t, r), "/elsewhere")
}

// The picker.

func TestPickerListsTheSessionAndAttachesByID(t *testing.T) {
	r := tmx(t, "1\n", nil)
	ok(t, r)
	mustContain(t, r.stderr, name) // the menu
	if strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("dry run is not one line: %q", r.stdout)
	}
	argv := shellSplit(t, r.stdout)
	if strings.Join(argv[:min(5, len(argv))], " ") != "env -u TMUX sh -c" || len(argv) != 6 {
		t.Fatalf("argv %q", argv)
	}
	mustContain(t, argv[5], "-L '"+socket+"'", "attach-session -f ignore-size -t '$", "= '"+name+"'")
}

func TestPickerNeverDetachesOrKills(t *testing.T) {
	r := tmx(t, "1\n", nil)
	ok(t, r)
	mustLack(t, r.stdout, "kill-", "attach-session -d", " -d ", "new-session")
}

func TestPickerStatusRowBadNumberAndEOFAttachNothing(t *testing.T) {
	for _, tc := range []struct{ stdin, server string }{
		{"1\n", empty}, {"2\n", socket}, {"0\n", socket},
		{"abc\n", socket}, {"1; id\n", socket}, {"", socket},
		{"0000000001\n", socket}, {" \n", socket},
	} {
		r := tmx(t, tc.stdin, func(a *app) { a.servers = []string{tc.server} })
		if r.stdout != "" || r.rc == 0 {
			t.Errorf("stdin %q server %s: rc %d stdout %q", tc.stdin, tc.server, r.rc, r.stdout)
		}
	}
}

func TestPickerTrimsWhitespaceAroundTheNumber(t *testing.T) {
	// The zsh read -r this replaces took " 1" and "1 " as 1.
	for _, in := range []string{" 1\n", "1 \n", "\t1\r\n", " 1 "} {
		r := tmx(t, in, nil)
		if r.rc != 0 || !strings.Contains(r.stdout, "attach-session") {
			t.Errorf("stdin %q: rc %d stdout %q", in, r.rc, r.stdout)
		}
	}
}

func TestPickerEmptyServerShowsAStatusRow(t *testing.T) {
	r := tmx(t, "", func(a *app) { a.servers = []string{empty} })
	if !regexp.MustCompile(`1\) ` + self + `\s+no sessions`).MatchString(r.stderr) {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestPickerEmptyHostOrServerListSaysSo(t *testing.T) {
	for _, tweak := range []func(*app){
		func(a *app) { a.hosts = nil },
		func(a *app) { a.servers = nil },
	} {
		r := tmx(t, "", tweak)
		if r.rc != 1 || r.stdout != "" || !strings.Contains(r.stderr, "is empty") {
			t.Errorf("%+v", r)
		}
	}
}

func TestPickerSwitchesWhenAlreadyAClientOfThatServer(t *testing.T) {
	r := tmx(t, "1\n", func(a *app) { a.tmuxEnv = sockPath(socket) + ",1,0" })
	ok(t, r)
	argv := shellSplit(t, r.stdout)
	if len(argv) != 3 || argv[0] != "sh" || argv[1] != "-c" {
		t.Fatalf("argv %q", argv)
	}
	mustContain(t, argv[2], "switch-client -t '$", "= '"+name+"'")
	mustLack(t, argv[2], "attach-session")
	mustLack(t, r.stderr, "nested")
}

func TestPickerNestsWithAWarningInsideAnotherServer(t *testing.T) {
	r := tmx(t, "1\n", func(a *app) { a.tmuxEnv = "/nonexistent/tmux-0/default,1,0" })
	ok(t, r)
	mustContain(t, r.stderr, "nested in tmux")
	mustContain(t, r.stdout, "env -u TMUX sh -c", "attach-session")
}

func TestPickerHostWithoutIgnoreSize(t *testing.T) {
	r := tmx(t, "1\n", func(a *app) { a.hosts = []Host{{Name: self}} })
	ok(t, r)
	argv := shellSplit(t, r.stdout)
	mustContain(t, argv[len(argv)-1], "attach-session -t '$")
	mustLack(t, r.stdout, "ignore-size")
}

func TestPickerRemoteHostsAndStatusReasons(t *testing.T) {
	hosts := []Host{{Name: self, IgnoreSize: true}, {Name: "fakeok", IgnoreSize: true},
		{Name: "fakekey"}, {Name: "fakeauth"}, {Name: "fakedown"}}
	r := tmx(t, "2\n", func(a *app) { a.hosts = hosts })
	ok(t, r)
	for _, re := range []string{
		`1\) ` + self + `\s+` + socket + `\s+` + name + `\s+1 win\s+0 attached`,
		`2\) fakeok\s+` + socket + `\s+` + name,
		`3\) fakekey\s+host key mismatch`,
		`4\) fakeauth\s+auth refused`,
		`5\) fakedown\s+unreachable`,
	} {
		if !regexp.MustCompile(re).MatchString(r.stderr) {
			t.Errorf("no %s in\n%s", re, r.stderr)
		}
	}
	argv := shellSplit(t, r.stdout)
	want := []string{fakeSSH, "-t", "-o", "ConnectTimeout=2", "-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "fakeok"}
	if len(argv) != len(want)+1 || strings.Join(argv[:len(want)], " ") != strings.Join(want, " ") {
		t.Fatalf("argv %q", argv)
	}
	mustContain(t, unwrapSh(t, argv[len(want)]), "attach-session -f ignore-size -t '$", "= '"+name+"'")

	// -r limits the picker to that host.
	r = tmx(t, "", func(a *app) { a.hosts = hosts }, "-r", "fakekey")
	if strings.Contains(r.stderr, self) || !strings.Contains(r.stderr, "1) fakekey") {
		t.Errorf("-r listed more than its host:\n%s", r.stderr)
	}
	r = tmx(t, "", func(a *app) { a.hosts = hosts }, "-l")
	if strings.Contains(r.stderr, "fake") || !strings.Contains(r.stderr, "1) "+self) {
		t.Errorf("-l listed more than this host:\n%s", r.stderr)
	}
}

func TestPickerHostNotInTheConfig(t *testing.T) {
	r := tmx(t, "", nil, "-r", "nope")
	if r.rc != 2 || r.stderr != "tmx: nope is not in ~/.config/tmx.conf (hosts: "+self+")\n" {
		t.Errorf("%+v", r)
	}
}

func TestPickerTimeoutKillsTheWholeJob(t *testing.T) {
	_ = os.Remove(hangPids)
	start := time.Now()
	r := tmx(t, "", func(a *app) {
		a.hosts = []Host{{Name: "fakehang"}}
		a.listTimeout = 500 * time.Millisecond
	})
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("listing took %v", took)
	}
	if !regexp.MustCompile(`1\) fakehang\s+timed out`).MatchString(r.stderr) {
		t.Errorf("stderr %q", r.stderr)
	}
	b, err := os.ReadFile(hangPids)
	if err != nil {
		t.Fatalf("fake ssh never ran: %v", err)
	}
	for _, f := range strings.Fields(string(b)) {
		p, _ := strconv.Atoi(f)
		gone := false
		for range 50 {
			if syscall.Kill(p, 0) != nil {
				gone = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !gone {
			_ = syscall.Kill(p, syscall.SIGKILL)
			t.Errorf("pid %d outlived the deadline", p)
		}
	}
}

// Named sessions on another host, through the fake ssh.

func onSiblings(a *app) {
	a.hosts = []Host{{Name: self, IgnoreSize: true}, {Name: "fakeok", IgnoreSize: true},
		{Name: "fakedown"}, {Name: "fakehang"}}
	a.namedServer = siblings
}

func remoteScript(t *testing.T, r run, host string) string {
	t.Helper()
	argv := shellSplit(t, r.stdout)
	if len(argv) < 3 || argv[0] != fakeSSH || argv[1] != "-t" || argv[len(argv)-2] != host {
		t.Fatalf("argv %q", argv)
	}
	return unwrapSh(t, argv[len(argv)-1])
}

// unwrapSh checks that a command sent over ssh is `sh -c '<script>'` and
// returns the script.
func unwrapSh(t *testing.T, cmd string) string {
	t.Helper()
	w := shellSplit(t, cmd)
	if len(w) != 3 || w[0] != "sh" || w[1] != "-c" {
		t.Fatalf("not wrapped in sh -c: %q", cmd)
	}
	return w[2]
}

func TestRemoteNewSessionInRepo(t *testing.T) {
	r := tmx(t, "", onSiblings, "-r", "fakeok", "webapp-run")
	ok(t, r)
	repo := filepath.Join(fakeHome, "code/team/web/webapp")
	s := remoteScript(t, r, "fakeok")
	mustContain(t, s, "d="+q(repo), `[ -d "$d" ] || d=$HOME`,
		"s="+q(siblings), "n='webapp-run'", `new-session -s "$n" -c "$d"`)
	mustLack(t, s, "ignore-size")
	mustContain(t, r.stderr, "tmx: new session webapp-run in ~/code/team/web/webapp (repo)")
}

func TestRemoteNewSessionNextToSibling(t *testing.T) {
	r := tmx(t, "", onSiblings, "-r", "fakeok", "api-server-20261124")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(sibDir))
	mustContain(t, r.stderr, "in ~/sib-here (sibling api-server-001)")
}

func TestRemoteNewSessionFolderMapAndHome(t *testing.T) {
	r := tmx(t, "", func(a *app) {
		onSiblings(a)
		a.hosts[1].Folders = map[string]string{"docs-site": "code/team/docs-website"}
	}, "-r", "fakeok", "docs-site-2")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(fakeHome+"/code/team/docs-website"))
	mustContain(t, r.stderr, "(folder map docs-site)")

	r = tmx(t, "", onSiblings, "-r", "fakeok", "nothing-like-it")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(fakeHome))
	mustContain(t, r.stderr, "in ~ (home)")
}

func TestRemoteExistingSessionJustAttaches(t *testing.T) {
	r := tmx(t, "", onSiblings, "-r", "fakeok", "api-server-001")
	ok(t, r)
	s := remoteScript(t, r, "fakeok")
	mustContain(t, s, "n='api-server-001'", `attach-session -t "$id"`)
	mustLack(t, s, " -c ", "; d=")
	mustLack(t, r.stderr, "new session")
}

func TestRemoteDirSkipsTheLookup(t *testing.T) {
	// fakehang would time the lookup out; --dir must never ask.
	start := time.Now()
	r := tmx(t, "", onSiblings, "-r", "fakehang", "--dir", "/x y", "work")
	ok(t, r)
	if time.Since(start) > 2*time.Second {
		t.Error("--dir still looked the folder up")
	}
	mustContain(t, remoteScript(t, r, "fakehang"), "d='/x y'", `-c "$d"`)
}

func TestRemoteLookupFailureSaysWhy(t *testing.T) {
	r := tmx(t, "", onSiblings, "-r", "fakedown", "work")
	if r.rc != 1 || r.stdout != "" || !strings.Contains(r.stderr, "tmx: fakedown: unreachable") {
		t.Errorf("%+v", r)
	}
}

func TestRemoteSelfIsANamedSessionInTheSmartFolder(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", onSiblings, "-r", self, "wip-api-server-run")
	ok(t, r)
	// ~/api-server is a repo too, but a sibling comes first.
	startsIn(t, localScript(t, r), sibDir)
	mustLack(t, r.stdout, "ssh")

	r = tmx(t, "", onSiblings, "-r", self, "webapp-2")
	ok(t, r)
	startsIn(t, localScript(t, r), filepath.Join(fakeHome, "code/team/web/webapp"))

	// An existing session keeps its folder.
	r = tmx(t, "", onSiblings, "-r", self, "api-server-001")
	ok(t, r)
	startsIn(t, localScript(t, r), "")
}

// A new session on a host where no ~/*/.git matches. zsh aborts a script
// on an unmatched glob, so this only passes when the script runs under sh.
func TestRemoteLookupWithNoRepoAtAllFallsBackToHome(t *testing.T) {
	r := tmx(t, "", func(a *app) {
		a.hosts = []Host{{Name: "fakebare"}}
		a.namedServer = siblings
	}, "-r", "fakebare", "work")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakebare"), "d="+q(bareHome))
	mustContain(t, r.stderr, "in ~ (home)")
	mustLack(t, r.stderr, "failed")
}

func TestRemoteNewSessionInRepoNamedInsideTheName(t *testing.T) {
	r := tmx(t, "", onSiblings, "-r", "fakeok", "api-server-test9")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(filepath.Join(fakeHome, "api-server")))
	mustContain(t, r.stderr, "in ~/api-server (repo api-server)")
}

func TestRemoteScriptsAreWrappedForSh(t *testing.T) {
	// The attach, the create and the folder lookup all go through ssh as
	// one sh -c argument, whatever the name holds.
	odd := "it's $HOME ~ a:b"
	r := tmx(t, "", onSiblings, "-r", "fakeok", odd)
	ok(t, r)
	s := remoteScript(t, r, "fakeok")
	mustContain(t, s, "n="+q(odd))

	r = tmx(t, "1\n", func(a *app) { a.hosts = []Host{{Name: "fakeok"}} })
	ok(t, r)
	argv := shellSplit(t, r.stdout)
	mustContain(t, unwrapSh(t, argv[len(argv)-1]), "attach-session", "= '"+name+"'")
}

func TestLocalChosenFolderMissingSaysHere(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", func(a *app) {
		onSiblings(a)
		a.hosts[0].Folders = map[string]string{"ghost-proj": "no/such/folder"}
	}, "-r", self, "ghost-proj-1")
	ok(t, r)
	startsIn(t, localScript(t, r), "/some/dir")
	mustContain(t, r.stderr, "in /some/dir (here; chosen folder missing)")
	mustLack(t, r.stderr, "no/such")
}

// tmx NAME on this host, with the folder guessed the same way as for
// another host but looked up without ssh, and $PWD as the last resort.

func TestLocalNewSessionNextToSibling(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", onSiblings, "api-server-20261124")
	ok(t, r)
	// ~/api-server is a repo too, but a sibling comes first.
	startsIn(t, localScript(t, r), sibDir)
	mustContain(t, r.stderr, "tmx: new session api-server-20261124 in ~/sib-here (sibling api-server-001)")
	// Run here with sh, not through ssh: localScript fails on anything else,
	// and the sibling was found, which only a local gather could do.
	if argv := shellSplit(t, r.stdout); argv[0] != "sh" {
		t.Errorf("ran %q, not sh", argv[0])
	}
}

func TestLocalNewSessionInRepo(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", nil, "webapp-run")
	ok(t, r)
	repo := filepath.Join(fakeHome, "code/team/web/webapp")
	startsIn(t, localScript(t, r), repo)
	mustContain(t, r.stderr, "in ~/code/team/web/webapp (repo)")

	// A repo whose name is a whole part of the session name.
	r = tmx(t, "", nil, "api-server-test9")
	ok(t, r)
	startsIn(t, localScript(t, r), filepath.Join(fakeHome, "api-server"))
	mustContain(t, r.stderr, "in ~/api-server (repo api-server)")
}

func TestLocalNewSessionWithNoMatchStartsHere(t *testing.T) {
	// A tmx server that isn't running just means no sessions yet.
	notRunning := func(a *app) { a.namedServer = empty }
	for _, tc := range []struct {
		tweak func(*app)
		args  []string
	}{
		{onSiblings, []string{"nothing-like-it"}},
		{onSiblings, []string{"-r", self, "nothing-like-it"}},
		{notRunning, []string{"nothing-like-it"}},
	} {
		r := tmx(t, "", tc.tweak, tc.args...)
		ok(t, r)
		startsIn(t, localScript(t, r), "/some/dir")
		mustContain(t, r.stderr, "tmx: new session nothing-like-it in /some/dir (here)")
		mustLack(t, r.stderr, "failed")
	}
}

func TestLocalExistingSessionJoinsWithoutDir(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", onSiblings, "api-server-001")
	ok(t, r)
	s := localScript(t, r)
	mustContain(t, s, "n='api-server-001'")
	startsIn(t, s, "")
	if r.stderr != "" {
		t.Errorf("stderr %q", r.stderr)
	}
}

// With no PATH the lookup can't even start sh, so a quiet stderr proves
// --dir never tried it.
func TestLocalDirSkipsTheLookup(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", t.TempDir())
	r := tmx(t, "", onSiblings, "--dir", "/elsewhere", "webapp-run")
	ok(t, r)
	startsIn(t, localScript(t, r), "/elsewhere")
	if r.stderr != "" {
		t.Errorf("stderr %q", r.stderr)
	}
}

// A failed lookup must not cost the session: -c comes off, so tmux starts
// a new one in this folder and leaves an existing one as it is.
func TestLocalLookupFailureStillStartsHere(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := tmx(t, "", nil, "work")
	ok(t, r)
	startsIn(t, localScript(t, r), "")
	if r.stderr != "tmx: folder lookup: failed (rc 127); a new session starts here\n" {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestLocalInsideTheTmxServerCreatesInTheSmartFolder(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", func(a *app) {
		onSiblings(a)
		a.tmuxEnv = sockPath(siblings) + ",1,0"
	}, "api-server-77")
	ok(t, r)
	mustContain(t, r.stdout, "new-session -d -s api-server-77 -c "+sibDir+" -P -F", "switch-client -t =api-server-77")
	mustLack(t, r.stdout, "new-session -A")
	mustLack(t, r.stderr, "nested")
	mustContain(t, r.stderr, "(sibling api-server-001)")
}

// Rejoining a session whose name holds ':' or '.'. tmux creates such a
// session happily, but `new-session -A -s NAME` then reads the name as a
// target and fails; tmx must find the session by exact name instead.

func TestTmuxStillReadsAColonAsATarget(t *testing.T) {
	out, err := tmuxCmd(awkward, "new-session", "-A", "-d", "-s", "c:d").CombinedOutput()
	if err == nil {
		t.Skip("this tmux accepts the name; tmx's lookup by name is still correct, just not needed")
	}
	mustContain(t, string(out), "can't find window")
}

func TestExistingAwkwardNamesAreRejoinedInEveryPath(t *testing.T) {
	onAwkward := func(a *app) {
		a.namedServer = awkward
		a.hosts = []Host{{Name: self, IgnoreSize: true}, {Name: "fakeok"}}
	}
	nested := func(a *app) { onAwkward(a); a.tmuxEnv = "/nonexistent/tmux-0/default,1,0" }
	for _, n := range awkwardNames {
		for _, tc := range []struct {
			path  string
			tweak func(*app)
			args  []string
		}{
			{"outside tmux", onAwkward, []string{n}},
			{"outside tmux, --dir", onAwkward, []string{"--dir", "/no/such/dir", n}},
			{"nested", nested, []string{n}},
			{"nested, --dir", nested, []string{"--dir", "/no/such/dir", n}},
			{"remote", onAwkward, []string{"-r", "fakeok", n}},
			{"remote, --dir", onAwkward, []string{"-r", "fakeok", "--dir", "/no/such/dir", n}},
			{"this host by name", onAwkward, []string{"-r", self, n}},
		} {
			r := tmx(t, "", tc.tweak, tc.args...)
			ok(t, r)
			if got := runStart(t, r); got != n {
				t.Errorf("%s, %q: joined %q", tc.path, n, got)
			}
		}
	}
}

func TestNewAwkwardNameIsCreatedAsGiven(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	onAwkward := func(a *app) {
		a.namedServer = awkward
		a.hosts = []Host{{Name: self, IgnoreSize: true}, {Name: "fakeok"}}
		a.pwd = dir
	}
	for i, tc := range []struct {
		path string
		args []string
	}{
		{"outside tmux", []string{"%s"}},
		{"outside tmux, --dir", []string{"--dir", dir, "%s"}},
		{"remote", []string{"-r", "fakeok", "--dir", dir, "%s"}},
	} {
		n := fmt.Sprintf("new:%d.x 'y' $z ", i)
		args := append([]string(nil), tc.args...)
		args[len(args)-1] = n
		r := tmx(t, "", onAwkward, args...)
		ok(t, r)
		if got, want := runStart(t, r), n+"|"+dir; got != want {
			t.Errorf("%s: created %q, want %q", tc.path, got, want)
		}
	}
}

// With no folder to start from, home is the last resort and the note says so.
func TestLocalUnknownFolderFallsBackToHome(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", func(a *app) { onSiblings(a); a.pwd = "" }, "nothing-like-it")
	ok(t, r)
	startsIn(t, localScript(t, r), fakeHome)
	mustContain(t, r.stderr, "tmx: new session nothing-like-it in ~ (home)")
	mustLack(t, r.stderr, "in  (")
}

func TestDirTildeIsExpanded(t *testing.T) {
	t.Setenv("HOME", fakeHome)
	other, _ := filepath.Abs("~bob") // only ~ and ~/ are home; ~bob is a folder name
	for arg, want := range map[string]string{
		"--dir=~/x y": filepath.Join(fakeHome, "x y"),
		"--dir=~":     fakeHome,
		"--dir=~/":    fakeHome,
		"--dir=~bob":  other,
	} {
		r := tmx(t, "", nil, arg, "work")
		ok(t, r)
		startsIn(t, localScript(t, r), want)
	}

	// For another host the ~ is that host's home, so its script expands it.
	r := tmx(t, "", onSiblings, "-r", "fakeok", "--dir=~/x y", "work")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), `d="$HOME"'/x y'`)
	r = tmx(t, "", onSiblings, "-r", "fakeok", "--dir", "~", "work")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), `d="$HOME"; `)
}

// ssh reports a job's output even when a child it started still holds
// stdout; the child must not outlive it.
func TestPickerKeepsRowsAndKillsChildLeftByAJobThatExitedZero(t *testing.T) {
	_ = os.Remove(leakPids)
	r := tmx(t, "", func(a *app) { a.hosts = []Host{{Name: "fakeleak"}} }, "-r", "fakeleak")
	if !regexp.MustCompile(`1\) fakeleak\s+` + socket + `\s+leaky\s+1 win`).MatchString(r.stderr) {
		t.Errorf("row dropped: %q", r.stderr)
	}
	mustLack(t, r.stderr, "failed")
	b, err := os.ReadFile(leakPids)
	if err != nil {
		t.Fatalf("fake ssh never ran: %v", err)
	}
	p, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	for range 50 {
		if syscall.Kill(p, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(p, syscall.SIGKILL)
	t.Errorf("pid %d outlived the job", p)
}

func TestNewAppReadsTheEnvironment(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "tmx.conf")
	if err := os.WriteFile(conf, []byte("servers one two\nhost envhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sshConf := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(sshConf, []byte("Host envhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMX_CONF", conf)
	t.Setenv("TMX_SSH_CONFIG", sshConf)
	a := newApp()
	if a.confErr != nil || !a.confFound || len(a.hosts) != 2 || a.hosts[1].Name != "envhost" ||
		strings.Join(a.servers, " ") != "one two" || !a.sshConf.Has("envhost") || a.hosts[0].Name != a.self {
		t.Errorf("config not read from TMX_CONF / TMX_SSH_CONFIG: %+v", a)
	}

	// Unset: ~/.config/tmx.conf under HOME, which here has none.
	t.Setenv("HOME", t.TempDir())
	os.Unsetenv("TMX_CONF")
	os.Unsetenv("TMX_SSH_CONFIG")
	a = newApp()
	if a.confFound || a.confShown != "~/.config/tmx.conf" || a.sshShown != "~/.ssh/config" || len(a.hosts) != 1 ||
		strings.Join(a.servers, " ") != "default tmx" {
		t.Errorf("defaults: found %v shown %q %q hosts %+v servers %q", a.confFound, a.confShown, a.sshShown, a.hosts, a.servers)
	}

	t.Setenv("TMX_SSH", "/some/fake/ssh")
	t.Setenv("TMX_DRYRUN", "")
	a = newApp()
	if a.ssh != "/some/fake/ssh" || !a.dryRun {
		t.Errorf("ssh %q dryRun %v", a.ssh, a.dryRun)
	}

	// Unset: the system ssh and a real run. A set-but-empty TMX_SSH counts
	// as unset, a set-but-empty TMX_DRYRUN does not.
	t.Setenv("TMX_SSH", "")
	os.Unsetenv("TMX_DRYRUN")
	a = newApp()
	if a.ssh != "ssh" || a.dryRun {
		t.Errorf("ssh %q dryRun %v", a.ssh, a.dryRun)
	}
}

// The small flags and the exit codes.

func TestVersion(t *testing.T) {
	r := tmx(t, "", nil, "--version")
	ok(t, r)
	if r.stdout != "tmx dev\n" || r.stderr != "" {
		t.Errorf("%+v", r)
	}
}

// The Makefile stamps the version with -X main.version; a renamed or
// moved variable would make that a silent no-op, so build it the same way.
func TestVersionStampedByLinkerFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "tmx")
	if out, err := exec.Command("go", "build", "-ldflags", "-X main.version=v-test-1", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || string(out) != "tmx v-test-1\n" {
		t.Errorf("got %q, %v", out, err)
	}
}

func TestHostsListsThisMachineThenTheConfig(t *testing.T) {
	conf := "host server1\nhost " + self + "\n  ignore-size no\nhost buildbox\n"
	r := tmx(t, "", withConf(t, conf, ""), "--hosts")
	ok(t, r)
	if r.stdout != self+"\nserver1\nbuildbox\n" || r.stderr != "" {
		t.Errorf("%+v", r)
	}
	// No config at all: this machine alone, silently.
	r = tmx(t, "", withConf(t, "", ""), "--hosts")
	ok(t, r)
	if r.stdout != self+"\n" || r.stderr != "" {
		t.Errorf("%+v", r)
	}
}

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		why   string
		args  []string
		stdin string
		tweak func(*app)
		rc    int
	}{
		{"usage", []string{"-h"}, "", nil, 0},
		{"version", []string{"--version"}, "", nil, 0},
		{"hosts", []string{"--hosts"}, "", nil, 0},
		{"a session picked", nil, "1\n", nil, 0},
		{"a named session", []string{"work"}, "", nil, 0},
		{"a remote named session", []string{"-r", "fakeok", "work"}, "", onSiblings, 0},
		{"nothing picked", nil, "", nil, 1},
		{"a status row picked", nil, "1\n", func(a *app) { a.servers = []string{empty} }, 1},
		{"nothing to list", nil, "", func(a *app) { a.hosts = nil }, 1},
		{"remote lookup failed", []string{"-r", "fakedown", "work"}, "", onSiblings, 1},
		{"unknown option", []string{"-x"}, "", nil, 2},
		{"two names", []string{"a", "b"}, "", nil, 2},
		{"missing value", []string{"-r"}, "", nil, 2},
		{"--version with more", []string{"--version", "x"}, "", nil, 2},
		{"--hosts with more", []string{"--hosts", "-l"}, "", nil, 2},
		{"unknown host", []string{"-r", "nope"}, "", nil, 2},
		{"unknown host with a name", []string{"-r", "nope", "work"}, "", nil, 2},
		{"bad config", nil, "", withConf(t, "bogus\n", ""), 2},
		{"bad config, --hosts", []string{"--hosts"}, "", withConf(t, "bogus\n", ""), 2},
		{"bad config, doctor", []string{"doctor"}, "", withConf(t, "bogus\n", ""), 2},
		{"bad config still prints usage", []string{"-h"}, "", withConf(t, "bogus\n", ""), 0},
		{"bad config still prints the version", []string{"--version"}, "", withConf(t, "bogus\n", ""), 0},
		{"host not in ssh config", []string{"-r", "nossh"}, "", withConf(t, "host nossh\n", ""), 1},
		{"host not in ssh config, named", []string{"-r", "nossh", "work"}, "", withConf(t, "host nossh\n", ""), 1},
	} {
		r := tmx(t, tc.stdin, tc.tweak, tc.args...)
		if r.rc != tc.rc {
			t.Errorf("%s: rc %d, want %d\nstdout %s\nstderr %s", tc.why, r.rc, tc.rc, r.stdout, r.stderr)
		}
		if tc.rc == 2 && r.stdout != "" {
			t.Errorf("%s: a usage error printed to stdout: %q", tc.why, r.stdout)
		}
	}
	// A usage error shows the usage; an unknown host lists the ones in reach.
	mustContain(t, tmx(t, "", nil, "-x").stderr, "tmx: unknown option -x", "usage: tmx")
	mustContain(t, tmx(t, "", nil, "-r", "nope").stderr, "(hosts: "+self+")")
	// The error blames the flag that has to go alone, wherever it sits.
	mustContain(t, tmx(t, "", nil, "-l", "--version").stderr, "tmx: --version goes alone")
}

// withConf reads hosts and servers the way tmx does, from a tmx.conf and an
// ssh config in a temp home. conf "" means no tmx.conf at all; sshConf ""
// means one that names every fake host. The real servers are swapped for
// the test socket, so no test ever lists the caller's own tmux.
func withConf(t *testing.T, conf, sshConf string) func(*app) {
	t.Helper()
	home := t.TempDir()
	confPath := filepath.Join(home, ".config", "tmx.conf")
	if conf != "" {
		if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if sshConf == "" {
		sshConf = "Host fake*\n"
	}
	sshPath := filepath.Join(home, ".ssh", "config")
	if err := os.MkdirAll(filepath.Dir(sshPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sshPath, []byte(sshConf), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(a *app) {
		a.configure(confPath, sshPath, home)
		for i, s := range a.servers {
			if s == "default" || s == "tmx" {
				a.servers[i] = socket
			}
		}
	}
}

func sshCallLog(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(sshCalls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// The picker lists this machine first, then the config's hosts in file
// order; a host ssh has no entry for gets a status row and no ssh at all.
func TestPickerWithAConfig(t *testing.T) {
	_ = os.Remove(sshCalls)
	conf := "servers " + socket + "\nhost fakeok\nhost nossh\nhost fakedown\nhost " + self + "\n  ignore-size no\n"
	r := tmx(t, "1\n", withConf(t, conf, ""))
	ok(t, r)
	for _, re := range []string{
		`(?m)^1\) ` + self + `\s+` + socket + `\s+` + name,
		`(?m)^2\) fakeok\s+` + socket + `\s+` + name,
		`(?m)^3\) nossh\s+not in ~/\.ssh/config$`,
		`(?m)^4\) fakedown\s+unreachable$`,
	} {
		if !regexp.MustCompile(re).MatchString(r.stderr) {
			t.Errorf("no %s in\n%s", re, r.stderr)
		}
	}
	if strings.Count(r.stderr, ") ") != 4 {
		t.Errorf("want 4 rows, one per host:\n%s", r.stderr)
	}
	calls := strings.Join(sshCallLog(t), " ")
	if calls != "fakeok fakedown" && calls != "fakedown fakeok" {
		t.Errorf("ssh calls %q, want fakeok and fakedown only", calls)
	}
	// This machine's host line turned ignore-size off for its own row.
	argv := shellSplit(t, r.stdout)
	mustContain(t, argv[len(argv)-1], "attach-session -t '$")
	mustLack(t, r.stdout, "ignore-size")

	// -r for that host: no ssh either, exit 1, the same reason.
	_ = os.Remove(sshCalls)
	for _, args := range [][]string{{"-r", "nossh"}, {"-r", "nossh", "work"}, {"-r", "nossh", "--dir", "/x", "work"}} {
		r = tmx(t, "", withConf(t, conf, ""), args...)
		if r.rc != 1 || r.stdout != "" || r.stderr != "tmx: nossh: not in ~/.ssh/config\n" {
			t.Errorf("%q: %+v", args, r)
		}
	}
	if c := sshCallLog(t); len(c) != 0 {
		t.Errorf("ssh started for %q", c)
	}
}

// An ssh config that can't be read is said to be unreadable, with the
// reason, in the picker, in -r and in doctor; it is not the same as every
// host missing from it. ssh never runs either way.
func TestUnreadableSSHConfigIsReportedAsThat(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"a folder where the file should be", "is a directory"},
		{"no permission to read it", "permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			confPath := filepath.Join(home, ".config", "tmx.conf")
			sshPath := filepath.Join(home, ".ssh", "config")
			if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(confPath, []byte("servers "+socket+"\nhost fakeok\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.reason == "is a directory" {
				if err := os.MkdirAll(sshPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				if os.Geteuid() == 0 {
					t.Skip("root reads anything")
				}
				if err := os.MkdirAll(filepath.Dir(sshPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(sshPath, []byte("Host fakeok\n"), 0); err != nil {
					t.Fatal(err)
				}
			}
			tweak := func(a *app) { a.configure(confPath, sshPath, home) }
			want := "can't read ~/.ssh/config: " + tc.reason

			_ = os.Remove(sshCalls)
			r := tmx(t, "1\n", tweak)
			ok(t, r)
			if !regexp.MustCompile(`(?m)^2\) fakeok\s+` + regexp.QuoteMeta(want) + `$`).MatchString(r.stderr) {
				t.Errorf("picker:\n%s", r.stderr)
			}
			mustLack(t, r.stderr, "not in ")

			r = tmx(t, "", tweak, "-r", "fakeok")
			if r.rc != 1 || r.stdout != "" || r.stderr != "tmx: fakeok: "+want+"\n" {
				t.Errorf("-r: %+v", r)
			}

			r = tmx(t, "", tweak, "doctor")
			if r.rc != 1 {
				t.Errorf("doctor rc %d, want 1", r.rc)
			}
			mustContain(t, r.stdout, "ssh config: "+want+"\n", "fakeok    "+want+"\n", self+"  ok  tmux ")
			mustLack(t, r.stdout, "not in ")

			if c := sshCallLog(t); len(c) != 0 {
				t.Errorf("ssh started for %q", c)
			}
		})
	}
}

// A host line for this machine matches its name in any case, since a
// hostname may be capitalised where the config is not.
func TestThisMachineIsMatchedIgnoringCase(t *testing.T) {
	conf := "servers " + socket + "\nhost " + strings.ToUpper(self) + "\n  ignore-size no\n"
	r := tmx(t, "", withConf(t, conf, ""), "--hosts")
	ok(t, r)
	if r.stdout != self+"\n" {
		t.Errorf("--hosts %q, want this machine once, spelled as it is", r.stdout)
	}
	for _, args := range [][]string{{"-r", self}, {"-r", strings.ToUpper(self)}} {
		r = tmx(t, "1\n", withConf(t, conf, ""), args...)
		ok(t, r)
		mustLack(t, r.stdout, "ignore-size") // the line's setting reached the row
	}
}

// Folders from the config reach a new session on that host.
func TestConfigFoldersReachANewSession(t *testing.T) {
	conf := "host fakeok\n  folder docs-site code/team/docs-website\n"
	r := tmx(t, "", func(a *app) { withConf(t, conf, "")(a); a.namedServer = siblings }, "-r", "fakeok", "docs-site-2")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(fakeHome+"/code/team/docs-website"))
	mustContain(t, r.stderr, "(folder map docs-site)")

	// A host line for this machine gives local sessions its folders.
	t.Setenv("HOME", fakeHome)
	conf = "host " + self + "\n  folder ghost-proj no/such/folder\n  folder sib sib-here\n"
	r = tmx(t, "", func(a *app) { withConf(t, conf, "")(a); a.namedServer = siblings }, "sib-thing")
	ok(t, r)
	startsIn(t, localScript(t, r), sibDir)
	mustContain(t, r.stderr, "(folder map sib)")
}

// strip-prefix and strip-suffix from the config decide the base name, and
// without them only a trailing -<digits> comes off.
func TestConfigStripReachesANewSession(t *testing.T) {
	onConf := func(conf string) func(*app) {
		return func(a *app) { withConf(t, conf, "")(a); a.namedServer = siblings }
	}
	repo := filepath.Join(fakeHome, "code/team/web/webapp")

	r := tmx(t, "", onConf("strip-prefix try-\nstrip-suffix -demo\nhost fakeok\n"), "-r", "fakeok", "try-webapp-demo-4")
	ok(t, r)
	mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(repo))
	mustContain(t, r.stderr, "in ~/code/team/web/webapp (repo)")

	// The defaults: -demo stays part of the base name, so the repo is found
	// only as a part of it, not as an exact match.
	r = tmx(t, "", onConf("host fakeok\n"), "-r", "fakeok", "webapp-demo")
	ok(t, r)
	mustContain(t, r.stderr, "in ~/code/team/web/webapp (repo webapp)")
	// And a prefix stays too, so the try- session is no sibling's.
	r = tmx(t, "", onConf("host fakeok\n"), "-r", "fakeok", "try-api-server")
	ok(t, r)
	mustContain(t, r.stderr, "(repo api-server)")
	mustLack(t, r.stderr, "sibling")

	// The digits still come off by default.
	r = tmx(t, "", onConf("host fakeok\n"), "-r", "fakeok", "webapp-12")
	ok(t, r)
	mustContain(t, r.stderr, "in ~/code/team/web/webapp (repo)")
}

// A host's repos line replaces ~/code as the place repos are searched;
// home's direct children are still checked.
func TestConfigReposReachANewSession(t *testing.T) {
	onConf := func(conf string) func(*app) {
		return func(a *app) { withConf(t, conf, "")(a); a.namedServer = siblings }
	}
	for _, tc := range []struct {
		why, conf, session, dir, note string
	}{
		{"default: ~/code is searched", "host fakeok\n", "webapp-1",
			"code/team/web/webapp", "in ~/code/team/web/webapp (repo)"},
		{"default: ~/src is not", "host fakeok\n", "infra-1",
			"", "in ~ (home)"},
		{"repos src finds ~/src/infra", "host fakeok\n  repos src\n", "infra-1",
			"src/infra", "in ~/src/infra (repo)"},
		{"repos src drops ~/code", "host fakeok\n  repos src\n", "webapp-1",
			"", "in ~ (home)"},
		{"home's children stay with repos set", "host fakeok\n  repos src\n", "api-server-test9",
			"api-server", "in ~/api-server (repo api-server)"},
		{"a missing folder doesn't stop the others", "host fakeok\n  repos no-such src code\n", "webapp-1",
			"code/team/web/webapp", "in ~/code/team/web/webapp (repo)"},
	} {
		r := tmx(t, "", onConf(tc.conf), "-r", "fakeok", tc.session)
		ok(t, r)
		mustContain(t, remoteScript(t, r, "fakeok"), "d="+q(filepath.Join(fakeHome, tc.dir)))
		if !strings.Contains(r.stderr, tc.note) {
			t.Errorf("%s: stderr %q, want %q", tc.why, r.stderr, tc.note)
		}
	}

	// The same for this machine, from a host line naming it.
	t.Setenv("HOME", fakeHome)
	r := tmx(t, "", onConf("host "+self+"\n  repos src\n"), "infra-3")
	ok(t, r)
	startsIn(t, localScript(t, r), filepath.Join(fakeHome, "src/infra"))
	mustContain(t, r.stderr, "in ~/src/infra (repo)")
}

func TestBadConfigStopsWithItsLine(t *testing.T) {
	r := tmx(t, "", withConf(t, "host a\n  ignore-size maybe\n", ""))
	if r.rc != 2 || r.stdout != "" || r.stderr != "tmx: ~/.config/tmx.conf:2: ignore-size takes yes or no\n" {
		t.Errorf("%+v", r)
	}
}

func TestDoctorAllGood(t *testing.T) {
	conf := "servers " + socket + " " + empty + "\nhost fakeok\n"
	r := tmx(t, "", withConf(t, conf, ""), "doctor")
	ok(t, r)
	re := regexp.MustCompile(`^config: ~/\.config/tmx\.conf
` + self + `  ok  tmux [^,\n]+, servers: ` + socket + `
fakeok    ok  tmux [^,\n]+, servers: ` + socket + `
$`)
	if !re.MatchString(r.stdout) {
		t.Errorf("stdout:\n%s", r.stdout)
	}

	// No config: this machine alone, and doctor says so.
	r = tmx(t, "", withConf(t, "", ""), "doctor")
	ok(t, r)
	if !regexp.MustCompile(`^no ~/\.config/tmx\.conf: only this machine
` + self + `  ok  tmux [^\n]+
$`).MatchString(r.stdout) {
		t.Errorf("stdout:\n%s", r.stdout)
	}

	// A server that is not running is no problem.
	r = tmx(t, "", withConf(t, "servers "+empty+"\n", ""), "doctor")
	ok(t, r)
	mustContain(t, r.stdout, "servers: none\n")
}

func TestDoctorProblems(t *testing.T) {
	_ = os.Remove(sshCalls)
	conf := "servers " + socket + "\nhost fakeok\nhost fakeauth\nhost fakenotmux\nhost nossh\nhost fakedown\nhost fakehang\n"
	start := time.Now()
	r := tmx(t, "", func(a *app) { withConf(t, conf, "")(a); a.doctorTimeout = time.Second }, "doctor")
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("doctor took %v; hosts should be checked at once", took)
	}
	if r.rc != 1 {
		t.Errorf("rc %d, want 1", r.rc)
	}
	for _, want := range []string{
		"fakeok      ok  tmux ",
		"fakeauth    auth refused\n",
		"fakenotmux  no tmux\n",
		"nossh       not in ~/.ssh/config\n",
		"fakedown    unreachable\n",
		"fakehang    timed out\n",
	} {
		mustContain(t, r.stdout, want)
	}
	for _, h := range sshCallLog(t) {
		if h == "nossh" {
			t.Error("ssh started for a host not in the ssh config")
		}
	}

	// One bad host is enough for exit 1.
	r = tmx(t, "", withConf(t, "servers "+socket+"\nhost fakeauth\n", ""), "doctor")
	if r.rc != 1 {
		t.Errorf("rc %d with one bad host", r.rc)
	}
}

func TestDoctorLineIgnoresJunk(t *testing.T) {
	servers := []string{"default", "tmx"}
	for _, tc := range []struct {
		out  string
		want string
		good bool
	}{
		{"V:tmux 3.8\nR:2\n", "ok  tmux 3.8, servers: tmx", true},
		{"V:tmux 3.8\nR:1\nR:2\n", "ok  tmux 3.8, servers: default tmx", true},
		{"V:tmux 3.8\nR:0\nR:3\nR:x\nR:99999999999999999999\n", "ok  tmux 3.8, servers: none", true},
		{"V:tmux \x1b[31m3.8\n", "ok  tmux ?[31m3.8, servers: none", true},
		{"R:1\n", "no answer from the check", false},
	} {
		got, good := doctorLine(jobResult{out: []byte(tc.out)}, servers)
		if got != tc.want || good != tc.good {
			t.Errorf("doctorLine(%q) = %q %v, want %q %v", tc.out, got, good, tc.want, tc.good)
		}
	}
}
