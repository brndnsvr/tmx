package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

// Host is one machine whose tmux servers the picker lists.
type Host struct {
	Name string
	// IgnoreSize attaches picker clients with `-f ignore-size`, so they
	// never resize a window that a client attached the ordinary way is
	// using. A host that runs `window-size latest` wants it off: there the
	// flag leaves the window sized for someone else and shows dot fill.
	IgnoreSize bool
	// Folders maps a keyword in a session's base name to a folder under
	// the host's home, for any new session tmx starts on that host:
	// `tmx -r HOST NAME`, and `tmx NAME` on the host itself.
	Folders map[string]string
	// Repos are the folders under the host's home searched for git repos
	// when a new session's folder is picked. nil means defaultRepoDirs.
	Repos []string
}

// defaultRepoDirs is where repos are searched when a host names none.
// Home's direct children are checked on top of these, always.
func defaultRepoDirs() []string { return []string{"code"} }

// repoDirs is h's repo search folders, the default when it names none.
func (h Host) repoDirs() []string {
	if h.Repos == nil {
		return defaultRepoDirs()
	}
	return h.Repos
}

// defaultServers is what is listed on every host (`tmux -L <name>`) when
// the config names none. `default` is named outright because inside tmux,
// plain tmux would pick the current server. A fresh slice each time: a
// shared one would let a caller that edits its servers change the default
// for everyone after it.
func defaultServers() []string { return []string{"default", "tmx"} }

// The server `tmx NAME` creates sessions on, apart from the default one.
const namedServer = "tmx"

// Config is what ~/.config/tmx.conf says. Hosts keeps file order, which
// is picker order after this machine, and may name this machine too.
type Config struct {
	Found   bool
	Servers []string
	Hosts   []Host
	Strip   Strip
}

// loadConfig reads the config at path. A missing file is no error: it
// means this machine alone. shown is the path as error messages give it.
func loadConfig(path, shown string) (Config, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{Servers: defaultServers()}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("%s: %v", shown, errors.Unwrap(err))
	}
	defer f.Close()
	cfg, err := parseConfig(f, shown)
	cfg.Found = true
	return cfg, err
}

// stripComment drops a '#' that starts a word, and the rest of the line.
// A '#' inside a word stays, so a folder name may hold one.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

// parseConfig reads the line-based format: top-level `servers`,
// `strip-prefix`, `strip-suffix` and `host` lines, and indented
// `ignore-size`, `folder` and `repos` lines that belong to the host above
// them. The first mistake stops it, with its line number,
// because a half-read host list would quietly hide a host.
func parseConfig(r io.Reader, shown string) (Config, error) {
	cfg := Config{}
	cur := -1 // index into cfg.Hosts of the host indented lines belong to
	sizeSet := map[string]bool{}
	// A word list given once at the top level: no repeats inside it, and
	// not given twice, since a second line would quietly replace the first.
	// It returns what is wrong, or "".
	listProblem := func(kw string, args []string, have bool) string {
		if have {
			return kw + " given twice"
		}
		if len(args) == 0 {
			return kw + " needs at least one value"
		}
		for i, s := range args {
			for _, prev := range args[:i] {
				if s == prev {
					return kw + " lists " + s + " twice"
				}
			}
		}
		return ""
	}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		text := sc.Text()
		if n == 1 {
			// An editor that saves a BOM would otherwise make the first
			// keyword unknown, with nothing visible to blame.
			text = strings.TrimPrefix(text, "\ufeff")
		}
		bad := func(format string, args ...any) (Config, error) {
			return Config{}, fmt.Errorf("%s:%d: %s", shown, n, fmt.Sprintf(format, args...))
		}
		line := stripComment(text)
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		kw, args := f[0], f[1:]
		indented := line[0] == ' ' || line[0] == '\t'

		if !indented {
			// Anything at the top level ends the host block above it.
			cur = -1
			switch kw {
			case "servers":
				if cfg.Servers != nil {
					return bad("servers given twice")
				}
				if len(args) == 0 {
					return bad("servers needs at least one server name")
				}
				for i, s := range args {
					for _, prev := range args[:i] {
						if s == prev {
							return bad("server %s listed twice", s)
						}
					}
				}
				cfg.Servers = args
			case "strip-prefix":
				if why := listProblem(kw, args, cfg.Strip.Prefixes != nil); why != "" {
					return bad("%s", why)
				}
				cfg.Strip.Prefixes = args
			case "strip-suffix":
				if why := listProblem(kw, args, cfg.Strip.Suffixes != nil); why != "" {
					return bad("%s", why)
				}
				cfg.Strip.Suffixes = args
			case "host":
				if len(args) != 1 {
					return bad("host takes one name, got %d", len(args))
				}
				name := args[0]
				// A leading '-' would reach ssh as an option, and a pattern
				// is something ssh matches, not a host it can connect to.
				if strings.HasPrefix(name, "-") || strings.ContainsAny(name, "*?!") {
					return bad("host %s is not a plain ssh host name", name)
				}
				// ssh aliases are case sensitive, but two rows that differ
				// only in case are nearly always one box listed twice.
				for _, h := range cfg.Hosts {
					if strings.EqualFold(h.Name, name) {
						if h.Name == name {
							return bad("duplicate host %s", name)
						}
						return bad("host %s duplicates %s (names differing only in case)", name, h.Name)
					}
				}
				cfg.Hosts = append(cfg.Hosts, Host{Name: name, IgnoreSize: true})
				cur = len(cfg.Hosts) - 1
			case "ignore-size", "folder", "repos":
				return bad("%s belongs to a host: indent it under a host line", kw)
			default:
				return bad("unknown keyword %s", kw)
			}
			continue
		}

		if cur < 0 {
			return bad("indented line with no host above it")
		}
		h := &cfg.Hosts[cur]
		switch kw {
		case "ignore-size":
			if len(args) != 1 || (args[0] != "yes" && args[0] != "no") {
				return bad("ignore-size takes yes or no")
			}
			if sizeSet[h.Name] {
				return bad("ignore-size given twice for %s", h.Name)
			}
			sizeSet[h.Name] = true
			h.IgnoreSize = args[0] == "yes"
		case "folder":
			if len(args) != 2 {
				return bad("folder takes KEYWORD PATH")
			}
			kwd, p := args[0], args[1]
			// The path is joined to the host's home, so a leading / or ~
			// would silently point somewhere else.
			if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
				return bad("folder path %s is relative to home; drop the leading %c", p, p[0])
			}
			if _, dup := h.Folders[kwd]; dup {
				return bad("folder %s given twice for %s", kwd, h.Name)
			}
			if h.Folders == nil {
				h.Folders = map[string]string{}
			}
			h.Folders[kwd] = p
		case "repos":
			if h.Repos != nil {
				return bad("repos given twice for %s", h.Name)
			}
			if len(args) == 0 {
				return bad("repos needs at least one folder")
			}
			for i, p := range args {
				// Joined to the host's home, like a folder path.
				if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
					return bad("repos folder %s is relative to home; drop the leading %c", p, p[0])
				}
				// "." is home itself; ".." would leave it.
				if c := path.Clean(p); c == ".." || strings.HasPrefix(c, "../") {
					return bad("repos folder %s must stay under home", p)
				}
				args[i] = path.Clean(p)
				for _, prev := range args[:i] {
					if p == prev {
						return bad("repos lists %s twice", p)
					}
				}
			}
			h.Repos = args
		case "host", "servers", "strip-prefix", "strip-suffix":
			return bad("%s must not be indented", kw)
		default:
			return bad("unknown keyword %s", kw)
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, fmt.Errorf("%s: %v", shown, err)
	}
	if cfg.Servers == nil {
		cfg.Servers = defaultServers()
	}
	return cfg, nil
}

// hostList puts this machine first, then the configured hosts in file
// order. A host line naming this machine adds no second row; its settings
// apply to this machine's own row instead. The match ignores case, since a
// machine's hostname may be capitalised where the config is not; the row
// keeps the machine's own spelling.
func hostList(self string, configured []Host) []Host {
	me := Host{Name: self, IgnoreSize: true}
	var rest []Host
	for _, h := range configured {
		if strings.EqualFold(h.Name, self) {
			me = h
			me.Name = self
			continue
		}
		rest = append(rest, h)
	}
	return append([]Host{me}, rest...)
}

// shortHost is the name this machine goes by in the config: its hostname
// cut at the first dot, so laptop.local is laptop.
func shortHost(h string) string {
	h, _, _ = strings.Cut(h, ".")
	return h
}
