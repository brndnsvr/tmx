package main

import (
	"path"
	"strings"
)

// Strip is what a session name loses on the way to its base name, from
// the config's strip-prefix and strip-suffix lines. Both are empty unless
// the config sets them.
type Strip struct {
	Prefixes []string
	Suffixes []string
}

// baseName strips what varies between sessions of the same project: the
// first of st's prefixes that matches, then any run of trailing st
// suffixes or `-<digits>`, in any order. A trailing `-<digits>` always goes,
// since a counter or a date is never part of a project's name. With
// strip-suffix -run, webapp-run and webapp-001 share webapp.
func baseName(name string, st Strip) string {
	for _, p := range st.Prefixes {
		if s, ok := strings.CutPrefix(name, p); ok {
			name = s
			break
		}
	}
	for {
		if s, ok := cutAnySuffix(name, st.Suffixes); ok {
			name = s
			continue
		}
		i := strings.LastIndexByte(name, '-')
		if i < 0 || !digits(name[i+1:]) {
			return name
		}
		name = name[:i]
	}
}

// cutAnySuffix drops the first of suffixes that name ends with.
func cutAnySuffix(name string, suffixes []string) (string, bool) {
	for _, x := range suffixes {
		if s, ok := strings.CutSuffix(name, x); ok {
			return s, true
		}
	}
	return name, false
}

// Session is one session on a host's tmx server, as the folder pick sees
// it: the active pane's folder and when it was last used.
type Session struct {
	Name     string
	Path     string
	Activity int64
}

// Folder is where a new session starts and why, for the one-line note.
type Folder struct {
	Dir string
	Why string
}

// Repos under these are copies or leftovers, never the project itself. A
// general rule, not one setup's: any path holding _archive, .trash,
// node_modules or .worktrees, or a folder whose name starts with OLD_.
var skipRepo = []string{"_archive", "/OLD_", ".trash", "node_modules", ".worktrees"}

// pickFolder chooses where a new session NAME starts on a host, taking the
// first rule that applies: the host's keyword map, a sibling session of
// the same project, a git repo named like the project (or whose name is a
// whole part of it), then the fallback. The fallback differs by caller: a
// remote host has only home to offer, while on this host the folder tmx was
// run from is the better guess. folders holds paths relative to home; repos
// holds repo folders (the parents of their .git); st is how a name becomes
// its base name.
func pickFolder(name, home string, folders map[string]string, sessions []Session, repos []string, st Strip, fallback Folder) Folder {
	base := baseName(name, st)
	if base == "" {
		return fallback
	}

	// The longest keyword is the most specific; ties go alphabetically so
	// the result never depends on map order.
	best := ""
	for kw := range folders {
		if kw == "" || !strings.Contains(base, kw) {
			continue
		}
		if len(kw) > len(best) || (len(kw) == len(best) && kw < best) {
			best = kw
		}
	}
	if best != "" {
		return Folder{path.Join(home, folders[best]), "folder map " + best}
	}

	// The most recently active sibling is most likely where work is now.
	var sib *Session
	for i := range sessions {
		s := &sessions[i]
		if s.Name == name || s.Path == "" || baseName(s.Name, st) != base {
			continue
		}
		if sib == nil || s.Activity > sib.Activity || (s.Activity == sib.Activity && s.Name < sib.Name) {
			sib = s
		}
	}
	if sib != nil {
		return Folder{sib.Path, "sibling " + sib.Name}
	}

	// The shortest path is the main checkout rather than a nested copy.
	var exact []string
	for _, r := range repos {
		if path.Base(r) == base && !containsAny(r, skipRepo) {
			exact = append(exact, r)
		}
	}
	if r, ok := shortest(exact); ok {
		return Folder{r, "repo"}
	}

	// No repo is named like the project, so take one whose name is a whole
	// run of its segments: api-server-test9 belongs with api-server, but a
	// repo called ops has nothing to do with netops. The longest name is
	// the most specific.
	var near []string
	nearLen := 0
	for _, r := range repos {
		n := path.Base(r)
		if containsAny(r, skipRepo) || !hasSegments(base, n) {
			continue
		}
		if len(n) > nearLen {
			near, nearLen = nil, len(n)
		}
		if len(n) == nearLen {
			near = append(near, r)
		}
	}
	if r, ok := shortest(near); ok {
		return Folder{r, "repo " + path.Base(r)}
	}
	return fallback
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// tilde shortens a path under home for display.
func tilde(p, home string) string {
	if home != "" && home != "/" {
		if p == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(p, home+"/"); ok {
			return "~/" + rest
		}
	}
	return p
}

// shortest picks the shortest path, equal lengths going alphabetically so
// the result never depends on input order.
func shortest(paths []string) (string, bool) {
	if len(paths) == 0 {
		return "", false
	}
	best := paths[0]
	for _, p := range paths[1:] {
		if len(p) < len(best) || (len(p) == len(best) && p < best) {
			best = p
		}
	}
	return best, true
}

// hasSegments reports whether sub sits inside s as whole segments, where
// a segment ends at '-', '_' or the end of the text.
func hasSegments(s, sub string) bool {
	if sub == "" {
		return false
	}
	boundary := func(b byte) bool { return b == '-' || b == '_' }
	for from := 0; ; {
		i := strings.Index(s[from:], sub)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(sub)
		if (i == 0 || boundary(s[i-1])) && (end == len(s) || boundary(s[end])) {
			return true
		}
		from = i + 1
	}
}
