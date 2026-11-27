package main

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sshConfig is ~/.ssh/config with the files it includes, kept as written
// so that one question can be answered for a given name: would ssh apply a
// Host stanza, other than a catch-all, to it? That is all tmx asks. A host
// ssh has no stanza for is never tried, since ssh would fall back to a DNS
// lookup and whatever default user and key it finds.
//
// The answer depends on the name, because ssh reads an Include inside a
// Host block only when that block matches the name. So the file is kept
// as a tree and walked per name, not flattened into one list of patterns.
type sshConfig struct {
	items []sshItem
}

type sshKind int

const (
	kindHost sshKind = iota
	kindMatch
	kindInclude
)

// sshItem is one Host, Match or Include line; every other keyword changes
// nothing tmx asks about and is dropped.
type sshItem struct {
	kind  sshKind
	pats  []string    // Host: the patterns on the line, case as written
	files [][]sshItem // Include: what each file the line named holds
}

// ssh itself gives up past 16 nested includes; the cap here only has to
// stop a file that includes itself.
const maxIncludeDepth = 16

// loadSSHConfig reads path and follows its Include lines the way ssh does
// for a user config: relative paths are under sshDir (~/.ssh), a leading ~
// is home, and globs expand in sorted order. A missing file means no
// hosts. Only an unreadable top file is an error; ssh skips included
// globs that match nothing, and tmx skips an unreadable include too.
func loadSSHConfig(path, sshDir, home string) (sshConfig, error) {
	items, err := readSSHFile(path, sshDir, home, 0)
	return sshConfig{items: items}, err
}

func readSSHFile(path, sshDir, home string, depth int) ([]sshItem, error) {
	f, err := os.Open(path)
	if err != nil {
		if depth == 0 && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, nil
	}
	defer f.Close()
	var items []sshItem
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		kw, args := sshLine(sc.Text())
		switch kw {
		case "host":
			items = append(items, sshItem{kind: kindHost, pats: args})
		case "match":
			items = append(items, sshItem{kind: kindMatch})
		case "include":
			if depth+1 > maxIncludeDepth {
				continue
			}
			it := sshItem{kind: kindInclude}
			for _, a := range args {
				if a == "~" {
					a = home
				} else if rest, ok := strings.CutPrefix(a, "~/"); ok {
					a = filepath.Join(home, rest)
				}
				if !filepath.IsAbs(a) {
					a = filepath.Join(sshDir, a)
				}
				for _, m := range sshGlob(a) {
					sub, _ := readSSHFile(m, sshDir, home, depth+1)
					it.files = append(it.files, sub)
				}
			}
			items = append(items, it)
		}
	}
	if err := sc.Err(); err != nil && depth == 0 {
		return nil, err
	}
	return items, nil
}

// sshGlob expands an Include pattern like glob(3), which ssh uses: unlike
// Go's filepath.Glob, a `*`, `?` or `[` never matches a name's leading
// dot, so `conf.d/*` skips `.hidden`. A component that starts with a dot
// itself still matches dot files. Results are sorted, as ssh reads them.
func sshGlob(pattern string) []string {
	pattern = filepath.Clean(pattern)
	matches, _ := filepath.Glob(pattern)
	pat := strings.Split(pattern, string(filepath.Separator))
	var out []string
	for _, m := range matches {
		parts := strings.Split(m, string(filepath.Separator))
		hidden := false
		if len(parts) == len(pat) {
			for i := range parts {
				if strings.HasPrefix(parts[i], ".") && !strings.HasPrefix(pat[i], ".") {
					hidden = true
				}
			}
		}
		if !hidden {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// sshLine splits one config line into its lowercased keyword and its
// arguments. ssh allows `Key value`, `Key=value` and `Key = value`, double
// or single quotes around an argument, and a '#' that starts a word begins
// a comment.
func sshLine(line string) (string, []string) {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return "", nil
	}
	end := strings.IndexAny(line, " \t=")
	if end < 0 {
		return strings.ToLower(line), nil
	}
	kw := strings.ToLower(line[:end])
	rest := strings.TrimLeft(line[end:], " \t")
	rest = strings.TrimPrefix(rest, "=")

	var args []string
	var cur strings.Builder
	inWord := false
	var quote byte
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		switch {
		case quote != 0 && ch == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(ch)
		case ch == '"' || ch == '\'':
			quote, inWord = ch, true
		case ch == ' ' || ch == '\t':
			if inWord {
				args = append(args, cur.String())
				cur.Reset()
				inWord = false
			}
		case ch == '#' && !inWord:
			i = len(rest)
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	if inWord {
		args = append(args, cur.String())
	}
	return kw, args
}

// Has reports whether ssh would apply a Host stanza, other than a
// catch-all, to host. It reads the file in order, as ssh does:
//   - a Host line starts a block, which applies to the name when a pattern
//     on the line matches and no `!` pattern does. Patterns match case
//     sensitively, against the name as typed;
//   - an Include before any block, or inside a block that applies to the
//     name, is followed. Inside one that doesn't apply, ssh reads the file
//     but nothing in it can match, so it is skipped;
//   - a Match line starts a block tmx can't evaluate (it may test the
//     user, an exec command, the address). It is treated as one that never
//     applies, so a Host line reachable only through an Include inside a
//     Match block is not counted. That can only make tmx say "not in ssh
//     config" for a host ssh would know, never the reverse.
//
// A catch-all pattern (only `*`) holds defaults for every host, which says
// nothing about whether this particular host is one ssh knows. A block
// with one still applies, so an Include in it is followed.
func (c sshConfig) Has(host string) bool {
	return hasHost(c.items, host)
}

func hasHost(items []sshItem, host string) bool {
	active := true // before any Host line, a file's lines are global
	for _, it := range items {
		switch it.kind {
		case kindHost:
			applies, names := hostLine(it.pats, host)
			if names {
				return true
			}
			active = applies
		case kindMatch:
			active = false
		case kindInclude:
			if !active {
				continue
			}
			for _, f := range it.files {
				if hasHost(f, host) {
					return true
				}
			}
		}
	}
	return false
}

// hostLine says whether a Host line applies to host, and whether it does
// so through a pattern that is not a catch-all. A matching `!` pattern
// rules the whole line out, wherever it sits.
func hostLine(pats []string, host string) (applies, names bool) {
	for _, p := range pats {
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if sshMatch(neg, host) {
				return false, false
			}
			continue
		}
		if sshMatch(p, host) {
			applies = true
			if strings.Trim(p, "*") != "" {
				names = true
			}
		}
	}
	return applies, names
}

// sshMatch is ssh's pattern match: `*` is any run of characters, `?` is
// any one, everything else matches itself.
func sshMatch(pattern, s string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Collapse a run of stars, then try every split point.
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if pattern == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if sshMatch(pattern, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
		default:
			if s == "" || s[0] != pattern[0] {
				return false
			}
		}
		pattern, s = pattern[1:], s[1:]
	}
	return s == ""
}
