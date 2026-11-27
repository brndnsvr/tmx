package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// doctorScript reports, in one trip, the host's tmux version and which of
// the servers are running. Servers go by index, as in listScript, so no
// name crosses the wire. Like listScript it exits 3 without a working
// tmux. list-sessions never starts a server, so asking is harmless.
func doctorScript(servers []string) string {
	var b strings.Builder
	b.WriteString(findTmux + "\n")
	b.WriteString(`v=$("$t" -V 2>/dev/null); case $v in tmux*) ;; *) exit 3 ;; esac` + "\n")
	b.WriteString(`printf 'V:%s\n' "$v"` + "\n")
	for i, s := range servers {
		fmt.Fprintf(&b, `"$t" -L %s list-sessions >/dev/null 2>&1 && echo R:%d`+"\n", q(s), i+1)
	}
	b.WriteString("exit 0\n")
	return b.String()
}

// doctorLine turns one host's answer into its report, and says whether
// the host is fine. Running no server is fine: the host is usable, it
// simply has no sessions yet.
func doctorLine(r jobResult, servers []string) (string, bool) {
	if why := classify(r.rc, r.timedOut, string(r.err)); why != "" {
		return why, false
	}
	version := ""
	var running []string
	for line := range strings.SplitSeq(string(r.out), "\n") {
		kind, rest, _ := strings.Cut(line, ":")
		switch kind {
		case "V":
			version = rest
		case "R":
			// Length first, so a huge index can't overflow.
			if !digits(rest) || len(rest) > 3 {
				continue
			}
			i, _ := strconv.Atoi(rest)
			if i >= 1 && i <= len(servers) {
				running = append(running, servers[i-1])
			}
		}
	}
	if version == "" {
		return "no answer from the check", false
	}
	list := "none"
	if len(running) > 0 {
		list = strings.Join(running, " ")
	}
	return "ok  " + sanitize(version) + ", servers: " + list, true
}

// doctor checks every host the picker would list, all at once, and prints
// one line per host. It exits 1 when any host has a problem, so a script
// can tell. A host missing from the ssh config (or an ssh config that can't be
// read) is a problem, and ssh is never started for it.
func (a *app) doctor() int {
	if a.confFound {
		fmt.Fprintf(a.stdout, "config: %s\n", a.confShown)
	} else {
		fmt.Fprintf(a.stdout, "no %s: only this machine\n", a.confShown)
	}
	good := true
	if a.sshErr != nil {
		fmt.Fprintf(a.stdout, "ssh config: %v\n", a.sshErr)
		good = false
	}

	script := doctorScript(a.servers)
	ctx, cancel := context.WithTimeout(context.Background(), a.doctorTimeout)
	defer cancel()
	lines := make([]string, len(a.hosts))
	oks := make([]bool, len(a.hosts))
	var wg sync.WaitGroup
	for i, h := range a.hosts {
		if why := a.sshProblem(h.Name); why != "" {
			lines[i] = why
			continue
		}
		wg.Go(func() {
			lines[i], oks[i] = doctorLine(runJob(ctx, a.hostCommand(h.Name, script)), a.servers)
		})
	}
	wg.Wait()

	w := 0
	for _, h := range a.hosts {
		w = max(w, len(h.Name))
	}
	for i, h := range a.hosts {
		fmt.Fprintf(a.stdout, "%-*s  %s\n", w, sanitize(h.Name), lines[i])
		good = good && oks[i]
	}
	if !good {
		return 1
	}
	return 0
}
