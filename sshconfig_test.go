package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSSHMatch(t *testing.T) {
	for _, tc := range []struct {
		pat, s string
		want   bool
	}{
		{"server", "server", true},
		{"server", "servers", false},
		{"server", "serve", false},
		{"*", "", true},
		{"*", "anything", true},
		{"se*", "server", true},
		{"se*", "se", true},
		{"*er", "server", true},
		{"*er", "servers", false},
		{"s*r", "server", true},
		{"s*r", "sr", true},
		{"s*r", "sx", false},
		{"s**r", "server", true},
		{"*.lan", "server.lan", true},
		{"*.lan", "server", false},
		{"se?ver", "server", true},
		{"se?ver", "sever", false},
		{"?", "", false},
		{"??", "ab", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"", "", true},
		{"", "a", false},
	} {
		if got := sshMatch(tc.pat, tc.s); got != tc.want {
			t.Errorf("sshMatch(%q, %q) = %v, want %v", tc.pat, tc.s, got, tc.want)
		}
	}
}

func TestSSHLine(t *testing.T) {
	for _, tc := range []struct {
		in   string
		kw   string
		args []string
	}{
		{"Host server1 laptop", "host", []string{"server1", "laptop"}},
		{"  host\tserver1", "host", []string{"server1"}},
		{"Host=server1", "host", []string{"server1"}},
		{"Host = server1", "host", []string{"server1"}},
		{`Include "my configs/*.conf" other`, "include", []string{"my configs/*.conf", "other"}},
		{"Include 'a b'", "include", []string{"a b"}},
		{"Host server1 # the server", "host", []string{"server1"}},
		{"Host a#b", "host", []string{"a#b"}},
		{"# Host commented", "", nil},
		{"", "", nil},
		{"Host", "host", nil},
		{"HostName 10.0.0.1", "hostname", []string{"10.0.0.1"}},
	} {
		kw, args := sshLine(tc.in)
		if kw != tc.kw || !reflect.DeepEqual(args, tc.args) {
			t.Errorf("sshLine(%q) = %q %q, want %q %q", tc.in, kw, args, tc.kw, tc.args)
		}
	}
}

func TestSSHConfigHas(t *testing.T) {
	dir := t.TempDir()
	conf := `# comment
Host *
  ServerAliveInterval 30

Host server1 laptop
  User me

Host *.lan !bad.lan
  User lan

Host dev-* !dev-secret
  ProxyJump laptop

Host * !nobody
  ForwardAgent no

Host * !buildbox !other
  Compression yes

Host **
  ForwardAgent no

Host * ***
  ForwardAgent no

Host ** knownstar
  ForwardAgent no

Match host matchonly exec "true"
  User m

Host Buildbox
  HostName 192.0.2.9

HOST Upper
  User u

Host se?ver-dev
  User s

Host !only-negated
`
	p := filepath.Join(dir, "config")
	if err := os.WriteFile(p, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := loadSSHConfig(p, dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"server1", true},
		{"laptop", true},
		{"box.lan", true},
		{"bad.lan", false}, // negated on its only line
		{"dev-one", true},  // a glob counts
		{"dev-secret", false},
		{"nobody", false},    // only Host * lines, which never count
		{"unknown", false},   // Host * and Host * !x say nothing about it
		{"matchonly", false}, // Match blocks are not Host lines
		{"Buildbox", true},
		{"buildbox", false}, // patterns are case-sensitive, like ssh
		{"BUILDBOX", false},
		{"Upper", true}, // the keyword is not
		{"upper", false},
		{"server-dev", true}, // ? is one character
		{"serrver-dev", false},
		{"only-negated", false},
		{"knownstar", true}, // ** beside a real pattern: the real one counts
		{"", false},
	} {
		if got := c.Has(tc.host); got != tc.want {
			t.Errorf("Has(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestSSHConfigIncludes(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Every Include sits before the first Host line, so it is always read.
	write(".ssh/config", `Include conf.d/*.conf
Include ~/elsewhere/abs-tilde
Include `+filepath.Join(home, "absolute")+`
Include missing-file nothing/*.conf
Include loop
Include .ssh-hidden/*.conf
Include conf.d/.dot*
Match all
  Include after-match
Host top
`)
	write(".ssh/conf.d/a.conf", "Host glob-a\n")
	write(".ssh/conf.d/b.conf", "Include nested/deeper\nHost glob-b\n")
	write(".ssh/conf.d/skip.txt", "Host not-globbed\n")
	write(".ssh/conf.d/.hidden.conf", "Host dot-hidden\n") // a * never matches a leading dot
	write(".ssh/conf.d/.dotted.conf", "Host dot-named\n")  // .dot* names the dot
	write(".ssh/nested/deeper", "Host nested-host\n")      // relative to ~/.ssh, not to b.conf
	write("elsewhere/abs-tilde", "Host tilde-host\n")
	write("absolute", "Host abs-host\n")
	write(".ssh/after-match", "Host after-match-host\n")
	// A file that includes itself must stop at the depth cap.
	write(".ssh/loop", "Host loop-host\nInclude loop\n")

	c, err := loadSSHConfig(filepath.Join(sshDir, "config"), sshDir, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"top", "glob-a", "glob-b", "nested-host", "tilde-host", "abs-host", "loop-host", "dot-named"} {
		if !c.Has(h) {
			t.Errorf("%s not found", h)
		}
	}
	for h, why := range map[string]string{
		"not-globbed":      "a file outside the glob was read",
		"dot-hidden":       "a * matched a leading dot",
		"after-match-host": "an Include inside a Match block was followed",
	} {
		if c.Has(h) {
			t.Errorf("%s found: %s", h, why)
		}
	}
	var count func(items []sshItem) int
	count = func(items []sshItem) int {
		n := 0
		for _, it := range items {
			switch {
			case it.kind == kindHost && len(it.pats) == 1 && it.pats[0] == "loop-host":
				n++
			case it.kind == kindInclude:
				for _, f := range it.files {
					n += count(f)
				}
			}
		}
		return n
	}
	if got := count(c.items); got != maxIncludeDepth {
		t.Errorf("loop file read %d times, want the cap %d", got, maxIncludeDepth)
	}
}

func TestSSHGlobSkipsDotNames(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a.conf", "b.conf", ".c.conf", "vis/in.conf", ".hid/in.conf", "x?y"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rel := func(ms []string) string {
		var out []string
		for _, m := range ms {
			r, _ := filepath.Rel(dir, m)
			out = append(out, r)
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct{ pat, want string }{
		{"*.conf", "a.conf b.conf"},
		{"?.conf", "a.conf b.conf"},
		{"[.a].conf", "a.conf"},
		{".*.conf", ".c.conf"},
		{".c.conf", ".c.conf"},
		{"*/in.conf", "vis/in.conf"},
		{".hid/*", ".hid/in.conf"},
		{"nothing*", ""},
	} {
		if got := rel(sshGlob(filepath.Join(dir, tc.pat))); got != tc.want {
			t.Errorf("sshGlob(%s) = %q, want %q", tc.pat, got, tc.want)
		}
	}
}

// Has must say what ssh does. Each case here is a config, and for each name
// `ssh -G` (which only prints the settings it would use, and never
// connects) is asked whether a non-catch-all Host stanza reached it: those
// stanzas all set `User hit`, and a catch-all sets nothing before the end
// of the file. Where tmx deliberately differs from ssh, the case says so
// and is not compared.
func TestSSHConfigAgreesWithSSH(t *testing.T) {
	sshBin, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("no ssh to compare with")
	}
	for _, tc := range []struct {
		name  string
		files map[string]string // @D@ is the temp dir; "config" is the one ssh is given
		names []string
	}{
		{"case is significant", map[string]string{"config": "Host Zcase\n User hit\n"},
			[]string{"Zcase", "zcase", "ZCASE"}},
		{"keyword is not case sensitive", map[string]string{"config": "HOST Zk\n User hit\nhost zl\n User hit\n"},
			[]string{"Zk", "zk", "zl", "ZL"}},
		{"star-only patterns", map[string]string{"config": "Host **\n ForwardAgent no\nHost ***\n ForwardAgent no\nHost known\n User hit\nHost *\n User dflt\n"},
			[]string{"anything", "known", "x"}},
		{"negation", map[string]string{"config": "Host zneg !zneg\n User hit\nHost a* !ab\n User hit\nHost * !zq\n ForwardAgent no\n"},
			[]string{"zneg", "aa", "ab", "zq", "other"}},
		{"? and a literal bracket", map[string]string{"config": "Host se?ver s[e]rver\n User hit\n"},
			[]string{"server", "se?ver", "sever", "s[e]rver", "sErver"}},
		{"include inside a block that does not match", map[string]string{
			"config":     "Host other\n User hit\n Include @D@/inner.conf\nHost *\n User dflt\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"other", "zinner", "whatever"}},
		{"include inside a block that matches", map[string]string{
			"config":     "Host zinc\n User hit\n Include @D@/inner.conf\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"zinc", "zinner"}},
		{"include inside a block, with a Host line of its own", map[string]string{
			"config":      "Host zinner\n User hit\n Include @D@/inner2.conf\n",
			"inner2.conf": "Host zdot\n User hit\n"},
			[]string{"zinner", "zdot"}},
		{"include before any block", map[string]string{
			"config":     "Include @D@/inner.conf\nHost top\n User hit\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"zinner", "top", "other"}},
		{"include inside a catch-all block", map[string]string{
			"config":     "Host *\n Include @D@/inner.conf\nHost *\n User dflt\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"zinner", "other"}},
		{"include inside a block that negation rules out", map[string]string{
			"config":     "Host * !zinner\n Include @D@/inner.conf\nHost *\n User dflt\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"zinner", "other"}},
		{"include after the block it was written under ended", map[string]string{
			"config":     "Host a\n User hit\nHost b\n User hit\n Include @D@/inner.conf\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"zinner", "a", "b"}},
		{"include inside an included file's block", map[string]string{
			"config":     "Include @D@/outer.conf\n",
			"outer.conf": "Host zouter\n User hit\nInclude @D@/inner.conf\n",
			"inner.conf": "Host zinner\n User hit\n"},
			[]string{"zouter", "zinner", "other"}},
		{"a Host line in an include does not end the block that included it", map[string]string{
			"config":      "Host *\n ForwardAgent no\n Include @D@/switch.conf\n Include @D@/inner.conf\n",
			"switch.conf": "Host zzz\n User hit\n",
			"inner.conf":  "Host zinner\n User hit\n"},
			[]string{"zinner", "zzz", "other"}},
		{"glob skips dot files", map[string]string{
			"config":         "Include @D@/d/*\n",
			"d/.hidden.conf": "Host zdot\n User hit\n",
			"d/vis.conf":     "Host zvis\n User hit\n"},
			[]string{"zdot", "zvis"}},
		{"glob with a dot in the pattern", map[string]string{
			"config":         "Include @D@/d/.hid*\n",
			"d/.hidden.conf": "Host zdot\n User hit\n",
			"d/vis.conf":     "Host zvis\n User hit\n"},
			[]string{"zdot", "zvis"}},
		{"glob does not enter dot folders", map[string]string{
			"config":        "Include @D@/*/in.conf\n",
			".hdir/in.conf": "Host zhdir\n User hit\n",
			"vdir/in.conf":  "Host zvdir\n User hit\n"},
			[]string{"zhdir", "zvdir"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				p := filepath.Join(dir, rel)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(strings.ReplaceAll(body, "@D@", dir)), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := filepath.Join(dir, "config")
			c, err := loadSSHConfig(cfg, dir, dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.names {
				out, err := exec.Command(sshBin, "-G", "-F", cfg, name).Output()
				if err != nil {
					t.Fatalf("ssh -G %q: %v", name, err)
				}
				want := false
				for line := range strings.SplitSeq(string(out), "\n") {
					if line == "user hit" {
						want = true
					}
				}
				if got := c.Has(name); got != want {
					t.Errorf("Has(%q) = %v, ssh -G says %v", name, got, want)
				}
			}
		})
	}
}

// tmx can't evaluate Match, so a Host line reached only through an Include
// under one is not counted, even where ssh would read it (Match all). The
// miss errs toward saying "not in ssh config", never toward trying an
// unknown host.
func TestSSHConfigIgnoresIncludesInMatchBlocks(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("inner.conf", "Host zinner\n")
	cfg := write("config", "Match all\n  Include "+filepath.Join(dir, "inner.conf")+"\n")
	c, err := loadSSHConfig(cfg, dir, dir)
	if err != nil || c.Has("zinner") {
		t.Errorf("Has(zinner) = %v, %v; an Include inside Match was followed", c.Has("zinner"), err)
	}
	// A Host line after the Match block ends it, and is counted again.
	cfg = write("config2", "Match all\n  Include "+filepath.Join(dir, "inner.conf")+"\nHost later\n  Include "+filepath.Join(dir, "inner.conf")+"\n")
	c, _ = loadSSHConfig(cfg, dir, dir)
	if !c.Has("later") || c.Has("zinner") {
		t.Errorf("later=%v zinner=%v", c.Has("later"), c.Has("zinner"))
	}
}

func TestSSHConfigMissingAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	c, err := loadSSHConfig(filepath.Join(dir, "none"), dir, dir)
	if err != nil || c.Has("anything") {
		t.Errorf("missing file: %v %+v", err, c)
	}
	// A folder where the file should be can't be read: say so.
	if _, err := loadSSHConfig(dir, dir, dir); err == nil {
		t.Error("a directory read as an ssh config")
	}
}
