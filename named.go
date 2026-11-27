package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// startScript is what runs on a host to create or rejoin NAME on a tmux
// server. It finds the session by exact name and attaches by its id,
// because `new-session -A -s NAME` and `attach -t NAME` read a ':' or '.'
// in NAME as a target ("can't find window: d"). Only when no session has
// that name does it create one, with `new-session -s NAME`, which takes
// the name as it is. One script serves this host, a nested tmux and ssh,
// and sh reads it everywhere, so the quoting is the same on all of them.
//
// dirExpr is a shell word for the new session's folder (already quoted),
// or empty to leave -c off. homeFallback starts in $HOME when that folder
// is gone, which only a remote host needs: the caller has no way to check
// it there.
func startScript(server, name, dirExpr string, homeFallback bool) string {
	parts := []string{
		findTmux,
		"n=" + q(name),
		"s=" + q(server),
		// read -r with IFS= keeps leading and trailing spaces; the compare
		// is a plain string test, so $ ' " and * in a name mean nothing.
		`id=$("$t" -u -L "$s" list-sessions -F '#{session_id}:#{session_name}' 2>/dev/null | while IFS= read -r l; do if [ "${l#*:}" = "$n" ]; then printf '%s\n' "${l%%:*}"; break; fi; done)`,
		`if [ -n "$id" ]; then exec "$t" -L "$s" attach-session -t "$id"; fi`,
	}
	create := `exec "$t" -L "$s" new-session -s "$n"`
	if dirExpr != "" {
		parts = append(parts, "d="+dirExpr)
		if homeFallback {
			parts = append(parts, `[ -d "$d" ] || d=$HOME`)
		}
		create += ` -c "$d"`
	}
	return strings.Join(append(parts, create), "; ")
}

// expandTilde turns a leading ~/ (or a lone ~) into home. zsh does that
// for `--dir ~/x` but not for `--dir=~/x`, which reaches tmx with the tilde
// still in it. ~user is left alone.
func expandTilde(dir, home string) string {
	if home == "" {
		return dir
	}
	if dir == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return dir
}

// remoteDirExpr is the shell word for a --dir given for another host. A
// leading ~ means that host's home, so the script expands it there.
func remoteDirExpr(dir string) string {
	if dir == "~" {
		return `"$HOME"`
	}
	if rest, ok := strings.CutPrefix(dir, "~/"); ok {
		return `"$HOME"` + q("/"+rest)
	}
	return q(dir)
}

// named creates or rejoins NAME on this host's tmx server. dir is where a
// new session starts; empty leaves -c off, so tmux starts a new session in
// the folder tmx runs in and leaves an existing session's folder alone.
func (a *app) named(name, dir string) int {
	t := localTmux()
	withDir := func(argv []string) []string {
		if dir != "" {
			return append(argv, "-c", dir)
		}
		return argv
	}
	sock := filepath.Join(a.tmuxTmpdirOrTmp(), "tmux-"+strconv.Itoa(os.Getuid()), a.namedServer)
	dirExpr := ""
	if dir != "" {
		dirExpr = q(dir)
	}
	script := startScript(a.namedServer, name, dirExpr, false)

	switch {
	case a.tmuxEnv == "":
		return a.finish([]string{"sh", "-c", script})
	case resolve(tmuxPrefix(a.tmuxEnv)) != resolve(sock):
		// Inside some other tmux server: nest, as the picker does.
		fmt.Fprintln(a.stderr, nestWarning)
		return a.finish([]string{"env", "-u", "TMUX", "sh", "-c", script})
	}

	// Already a client of the tmx server, where new-session -A would
	// refuse to nest: find or make the session, then move this client to
	// it. By id, since -t splits a name at ':'.
	id := ""
	out, _ := exec.Command(t, "-u", "-L", a.namedServer, "list-sessions", "-F", "#{session_id}:#{session_name}").Output()
	for line := range strings.SplitSeq(string(out), "\n") {
		if sid, sname, ok := strings.Cut(line, ":"); ok && sname == name {
			id = sid
			break
		}
	}
	if id == "" {
		// -P hands back the new id, so the name never goes through -t.
		create := append(withDir([]string{t, "-L", a.namedServer, "new-session", "-d", "-s", name}), "-P", "-F", "#{session_id}")
		if a.dryRun {
			fmt.Fprintln(a.stdout, quoteArgv(create))
			return a.finish([]string{t, "-L", a.namedServer, "switch-client", "-t", "=" + name})
		}
		out, err := exec.Command(create[0], create[1:]...).Output()
		if err != nil {
			fmt.Fprintf(a.stderr, "tmx: could not create %s: %v\n", name, err)
			return 1
		}
		id = strings.TrimSpace(string(out))
	}
	return a.finish([]string{t, "-L", a.namedServer, "switch-client", "-t", id})
}

func (a *app) tmuxTmpdirOrTmp() string {
	if a.tmuxTmpdir != "" {
		return a.tmuxTmpdir
	}
	return "/tmp"
}

// gathered is what one round trip to a host says about NAME's folder.
type gathered struct {
	home     string
	sessions []Session
	repos    []string
	exists   bool
}

// gatherScript collects, in one trip, the tmx server's sessions with
// their active pane's folder and activity, and the host's git repos: up to
// 4 levels under each of repoDirs (relative to home), plus home's direct
// children. Two listings joined by id, because tmux turns a tab in -F
// output into '_' and both a name and a path may hold ':'; each goes last
// on its own line. -u keeps non-ASCII paths intact when ssh brings no
// UTF-8 locale. The repo search is skipped when NAME already exists, since
// nothing will use it.
func gatherScript(server, name string, repoDirs []string) string {
	l := `"$t" -u -L ` + q(server) + ` list-sessions -F `
	// A folder that doesn't exist only makes find complain on stderr; the
	// others are still searched.
	roots := ""
	for _, d := range repoDirs {
		if d == "." {
			// Home itself, without the "/." that would show in every path.
			roots += ` "$HOME"`
			continue
		}
		roots += ` "$HOME"/` + q(d)
	}
	return strings.Join([]string{
		findTmux,
		checkTmux,
		`printf 'H:%s\n' "$HOME"`,
		l + `'S:#{session_id}:#{session_activity}:#{session_name}' 2>/dev/null`,
		l + `'P:#{session_id}:#{pane_current_path}' 2>/dev/null`,
		`if ! ` + l + `'N:#{session_name}' 2>/dev/null | grep -qxF -e ` + q("N:"+name) + `; then`,
		`  find` + roots + ` -maxdepth 4 -name .git 2>/dev/null | sed 's/^/R:/'`,
		`  for g in "$HOME"/*/.git; do [ -e "$g" ] && printf 'R:%s\n' "$g"; done`,
		`fi`,
		`exit 0`,
	}, "\n")
}

func parseGather(out, name string) gathered {
	var g gathered
	byID := map[string]*Session{}
	var order []string
	paths := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		kind, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch kind {
		case "H":
			g.home = rest
		case "S":
			f := strings.SplitN(rest, ":", 3)
			if len(f) != 3 || !strings.HasPrefix(f[0], "$") || !digits(f[0][1:]) || !digits(f[1]) {
				continue
			}
			act, _ := strconv.ParseInt(f[1], 10, 64)
			byID[f[0]] = &Session{Name: f[2], Activity: act}
			order = append(order, f[0])
		case "P":
			id, p, ok := strings.Cut(rest, ":")
			if ok && strings.HasPrefix(p, "/") {
				paths[id] = p
			}
		case "R":
			// Two search roots can find the same repo (home's children and a
			// `repos .`); count it once.
			if dir, ok := strings.CutSuffix(rest, "/.git"); ok && strings.HasPrefix(dir, "/") && !slices.Contains(g.repos, dir) {
				g.repos = append(g.repos, dir)
			}
		}
	}
	for _, id := range order {
		s := byID[id]
		s.Path = paths[id]
		if s.Name == name {
			g.exists = true
		}
		g.sessions = append(g.sessions, *s)
	}
	return g
}

// gather runs gatherScript for NAME on host h: through sh here, through
// ssh elsewhere. why is empty when the round trip worked.
func (a *app) gather(h Host, name string) (g gathered, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), a.gatherTimeout)
	defer cancel()
	r := runJob(ctx, a.hostCommand(h.Name, gatherScript(a.namedServer, name, h.repoDirs())))
	if why := classify(r.rc, r.timedOut, string(r.err)); why != "" {
		return gathered{}, why
	}
	return parseGather(string(r.out), name), ""
}

// note prints the one line that says where a new session starts and why.
func (a *app) note(name, dir, home, why string) {
	fmt.Fprintf(a.stderr, "tmx: new session %s in %s (%s)\n", sanitize(name), sanitize(tilde(dir, home)), sanitize(why))
}

// localNamed creates or rejoins NAME on this host's tmx server. dir, when
// given, is where a new session starts; otherwise a new session starts in
// the folder pickFolder chooses, falling back to the folder tmx was run
// from, and an existing one is joined as it is.
func (a *app) localNamed(h Host, name, dir string) int {
	if dir != "" {
		dir = expandTilde(dir, os.Getenv("HOME"))
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		return a.named(name, dir)
	}
	g, why := a.gather(h, name)
	if why != "" {
		// Leaving -c off still starts a new session here, since tmux uses
		// the client's folder, and leaves an existing one alone. Better
		// than refusing a session over a folder guess.
		fmt.Fprintf(a.stderr, "tmx: folder lookup: %s; a new session starts here\n", why)
		return a.named(name, "")
	}
	if g.exists {
		return a.named(name, "")
	}
	// The folder tmx runs in is the last resort. If even that is unknown
	// (the folder was removed under us), home is all there is.
	last := Folder{a.pwd, "here"}
	if last.Dir == "" {
		last = Folder{g.home, "home"}
	}
	f := pickFolder(name, g.home, h.Folders, g.sessions, g.repos, a.strip, last)
	if f != last {
		if st, err := os.Stat(f.Dir); err != nil || !st.IsDir() {
			// The note must not name a folder the session is not going to
			// be in.
			f = Folder{last.Dir, last.Why + "; chosen folder missing"}
		}
	}
	if f.Dir != "" {
		a.note(name, f.Dir, g.home, f.Why)
	}
	return a.named(name, f.Dir)
}

// remoteNamed creates or rejoins NAME on another host's tmx server,
// attaching the ordinary way. A new session starts in dir, or in the folder
// pickFolder chooses when dir is empty, falling back to home.
func (a *app) remoteNamed(h Host, name, dir string) int {
	if dir == "" {
		g, why := a.gather(h, name)
		if why != "" {
			fmt.Fprintf(a.stderr, "tmx: %s: %s\n", h.Name, why)
			return 1
		}
		if !g.exists {
			f := pickFolder(name, g.home, h.Folders, g.sessions, g.repos, a.strip, Folder{g.home, "home"})
			dir = f.Dir
			a.note(name, dir, g.home, f.Why)
		}
	}

	// The folder may be gone by the time the session starts, and only the
	// remote script can tell, so it falls back to home there (the note
	// above still names the folder that was chosen).
	dirExpr := ""
	if dir != "" {
		dirExpr = remoteDirExpr(dir)
	}
	script := startScript(a.namedServer, name, dirExpr, true)
	if a.tmuxEnv != "" {
		fmt.Fprintln(a.stderr, nestWarning)
	}
	argv := append([]string{a.ssh, "-t"}, sshOpts...)
	return a.finish(append(argv, h.Name, viaSh(script)))
}
