package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Never prompt: the picker waits for the slowest host, so a host without
// key auth must fail at once ("auth refused") rather than ask for a
// password. ConnectTimeout bounds the TCP connect only; the listing
// deadline bounds everything else.
var sshOpts = []string{
	"-o", "ConnectTimeout=2", "-o", "BatchMode=yes",
	"-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no",
}

// A killed job must never leave a ControlPersist master behind, so the
// jobs the picker may kill never start one.
var sshJobOpts = append(append([]string{}, sshOpts...), "-o", "ControlMaster=no")

const nestWarning = "tmx: nested in tmux; press the prefix twice to reach the inner session"

// listScript lists every server on one host. Servers go by index, so no
// name crosses the wire, and the session name comes last because tmux 3.8
// lets it hold ':'. It exits 0 only at its end, and 3 without a working
// tmux: an exit code rather than a marker line, since a session can be
// named anything.
func listScript(servers []string) string {
	var b strings.Builder
	b.WriteString(findTmux + "\n" + checkTmux + "\n")
	for i, s := range servers {
		fmt.Fprintf(&b, `"$t" -u -L %s list-sessions -F '%d:#{session_windows}:#{session_attached}:#{session_id}:#{session_name}' 2>/dev/null`+"\n", q(s), i+1)
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// classify says why a host lists nothing, from its job's exit code and
// stderr; empty for a clean exit.
func classify(rc int, timedOut bool, stderr string) string {
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(stderr, s) {
				return true
			}
		}
		return false
	}
	switch {
	case timedOut:
		return "timed out"
	case rc == 0:
		return ""
	case rc == 3:
		return "no tmux"
	case rc == 255:
		switch {
		case has("Host key verification failed", "REMOTE HOST IDENTIFICATION HAS CHANGED"):
			return "host key mismatch"
		case has("Permission denied", "Too many authentication failures"):
			return "auth refused"
		case has("Could not resolve", "timed out", "No route", "unreachable", "refused", "closed by", "reset by"):
			return "unreachable"
		default:
			return "ssh failed"
		}
	default:
		return fmt.Sprintf("failed (rc %d)", rc)
	}
}

// entry is one picker row: a session, or a host's status row when status
// is set.
type entry struct {
	host, server, id, name string
	windows, attached      string
	status                 string
}

func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseLine checks one listing line and drops anything that isn't a
// well-formed session, since the text comes from another machine.
func parseLine(line string, servers []string) (entry, bool) {
	f := strings.SplitN(line, ":", 5)
	if len(f) != 5 || !digits(f[0]) || !digits(f[1]) || !digits(f[2]) ||
		!strings.HasPrefix(f[3], "$") || !digits(f[3][1:]) {
		return entry{}, false
	}
	// Length first, so a huge index can't overflow.
	if len(f[0]) > 3 {
		return entry{}, false
	}
	i, _ := strconv.Atoi(f[0])
	if i < 1 || i > len(servers) {
		return entry{}, false
	}
	return entry{server: servers[i-1], windows: f[1], attached: f[2], id: f[3], name: f[4]}, true
}

// sanitize swaps anything unprintable for '?' before remote text reaches
// the terminal.
func sanitize(s string) string {
	var b strings.Builder
	for i, w := 0, 0; i < len(s); i += w {
		r, size := utf8.DecodeRuneInString(s[i:])
		w = size
		if (r == utf8.RuneError && size <= 1) || !unicode.IsPrint(r) {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// list asks every host at once and waits about listTimeout in all, since
// ConnectTimeout bounds neither a name lookup (a powered-off `.local` host
// takes 5s to fail to resolve) nor a remote command that hangs.
func (a *app) list(hosts []Host) []entry {
	script := listScript(a.servers)
	ctx, cancel := context.WithTimeout(context.Background(), a.listTimeout)
	defer cancel()
	results := make([]jobResult, len(hosts))
	skip := make([]string, len(hosts))
	var wg sync.WaitGroup
	for i, h := range hosts {
		// A host ssh has no entry for is never tried: ssh would guess at
		// its address, user and key instead of failing.
		if why := a.sshProblem(h.Name); why != "" {
			skip[i] = why
			continue
		}
		wg.Go(func() {
			results[i] = runJob(ctx, a.hostCommand(h.Name, script))
		})
	}
	wg.Wait()

	var out []entry
	for i, h := range hosts {
		if skip[i] != "" {
			out = append(out, entry{host: h.Name, status: skip[i]})
			continue
		}
		r := results[i]
		why := classify(r.rc, r.timedOut, string(r.err))
		kept := 0
		if why == "" {
			for line := range strings.SplitSeq(string(r.out), "\n") {
				e, ok := parseLine(line, a.servers)
				if !ok {
					continue
				}
				e.host = h.Name
				out = append(out, e)
				kept++
			}
			// Count rows kept, not lines read: junk or a bad index keeps none.
			if kept == 0 {
				why = "no sessions"
			}
		}
		if why != "" {
			out = append(out, entry{host: h.Name, status: why})
		}
	}
	return out
}

// hostCommand runs a non-interactive script on a host: a local shell for
// this host, the system ssh (so its config, jumps and mux masters apply)
// for any other, with the script wrapped to run under sh there.
func (a *app) hostCommand(host, script string) []string {
	if host == a.self {
		return []string{"sh", "-c", script}
	}
	argv := append([]string{a.ssh}, sshJobOpts...)
	return append(argv, host, viaSh(script))
}

// layout renders rows and the header. Each column fits the widest text
// shown in it, sanitized first, and never narrower than its label. Caps let
// one long name overflow its own row alone rather than push windows and
// attached off every row.
func layout(es []entry) ([]string, string) {
	w := [4]int{4, 6, 7, 3}
	caps := [4]int{20, 20, 40, 6}
	cells := make([][5]string, len(es))
	for i, e := range es {
		c := [5]string{e.host, e.server, e.name, e.windows, e.attached}
		if e.status != "" {
			c = [5]string{e.host, "", "", "", e.status}
		}
		for k := range c {
			c[k] = sanitize(c[k])
			if k < 4 {
				w[k] = max(w[k], utf8.RuneCountInString(c[k]))
			}
		}
		cells[i] = c
	}
	for k := range w {
		w[k] = min(w[k], caps[k])
	}
	rows := make([]string, len(es))
	for i, c := range cells {
		if es[i].status != "" {
			rows[i] = fmt.Sprintf("%-*s  %s", w[0], c[0], c[4])
		} else {
			rows[i] = fmt.Sprintf("%-*s  %-*s  %-*s  %*s win  %s attached", w[0], c[0], w[1], c[1], w[2], c[2], w[3], c[3], c[4])
		}
	}
	header := fmt.Sprintf("%-*s  %-*s  %-*s  %-*s  attached", w[0], "host", w[1], "server", w[2], "session", w[3]+4, "windows")
	return rows, header
}

// choose returns the 1-based row picked, or 0 for no pick. fzf at a
// terminal; a numbered list when stdin is piped or fzf is absent.
func (a *app) choose(rows []string, header string) int {
	var menu bytes.Buffer
	for i, r := range rows {
		fmt.Fprintf(&menu, "%d\t%s\n", i+1, r)
	}
	var pick string
	if a.stdinTTY && a.fzf != "" {
		cmd := exec.Command(a.fzf, "--delimiter=\t", "--with-nth=2..", "--no-sort", "--reverse", "--header="+header)
		cmd.Stdin = &menu
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return 0
		}
		pick, _, _ = strings.Cut(string(out), "\t")
	} else {
		for i, r := range rows {
			fmt.Fprintf(a.stderr, "%d) %s\n", i+1, r)
		}
		if a.stdinTTY {
			fmt.Fprint(a.stderr, "session #: ")
		}
		line, err := bufio.NewReader(a.stdin).ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			return 0
		}
		pick = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	}
	// A shell's read takes " 1" and "1 " as 1, so a typed number may
	// carry spaces here too.
	pick = strings.TrimSpace(pick)
	if !digits(pick) || len(pick) > 9 {
		return 0
	}
	n, _ := strconv.Atoi(pick)
	if n < 1 || n > len(rows) {
		return 0
	}
	return n
}

// attachScript re-checks, just before attaching, that the id still
// carries the listed name, then hands over to tmux with the given command.
// It targets the id, since -t splits a name at ':'. One line, so dry-run
// output is one line too.
func attachScript(e entry, command string) string {
	sock := "-L " + q(e.server)
	return findTmux + `; [ "$("$t" -u ` + sock + ` display -p -t ` + q(e.id) + ` "#{session_name}" 2>/dev/null)" = ` +
		q(e.name) + ` ] || { echo "tmx: that session is gone" >&2; exit 1; }; exec "$t" ` + sock + " " + command
}

func (a *app) pick(hosts []Host) int {
	if len(hosts) == 0 || len(a.servers) == 0 {
		fmt.Fprintln(a.stderr, "tmx: the host or server list is empty; nothing to list")
		return 1
	}
	es := a.list(hosts)
	rows, header := layout(es)
	n := a.choose(rows, header)
	// Only a session row attaches; Esc, EOF, a status row or a bad number
	// don't.
	if n == 0 || es[n-1].status != "" {
		return 1
	}
	e := es[n-1]
	var h Host
	for _, x := range hosts {
		if x.Name == e.host {
			h = x
		}
	}
	id := q(e.id)

	if e.host == a.self && a.tmuxEnv != "" && a.sameServer(e) {
		// Already a client of that server: move this client rather than nest.
		return a.finish([]string{"sh", "-c", attachScript(e, "switch-client -t "+id)})
	}
	if a.tmuxEnv != "" {
		fmt.Fprintln(a.stderr, nestWarning)
	}
	attach := "attach-session -t " + id
	if h.IgnoreSize {
		attach = "attach-session -f ignore-size -t " + id
	}
	if e.host == a.self {
		return a.finish([]string{"env", "-u", "TMUX", "sh", "-c", attachScript(e, attach)})
	}
	argv := append([]string{a.ssh, "-t"}, sshOpts...)
	return a.finish(append(argv, e.host, viaSh(attachScript(e, attach))))
}

// sameServer reports whether this client's $TMUX is the server holding e.
func (a *app) sameServer(e entry) bool {
	out, err := exec.Command(localTmux(), "-L", e.server, "display", "-p", "-t", e.id, "#{socket_path}").Output()
	if err != nil {
		return false
	}
	sock := strings.TrimSpace(string(out))
	return sock != "" && resolve(sock) == resolve(tmuxPrefix(a.tmuxEnv))
}
