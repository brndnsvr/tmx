package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The default servers spelled out, so a test that edits what the code
// hands back can't also change what it is compared against.
func wantDefault() []string { return []string{"default", "tmx"} }

func TestParseConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want Config
	}{
		{"empty file: default servers, no hosts", "", Config{Servers: wantDefault()}},
		{"comments and blank lines only", "# a\n\n   \n\t# b\n", Config{Servers: wantDefault()}},
		{"servers", "servers default tmx other\n", Config{Servers: []string{"default", "tmx", "other"}}},
		{"one server", "servers tmx\n", Config{Servers: []string{"tmx"}}},
		{
			"hosts keep file order and default to ignore-size yes",
			"host server1\nhost devbox\nhost buildbox\n",
			Config{Servers: wantDefault(), Hosts: []Host{
				{Name: "server1", IgnoreSize: true}, {Name: "devbox", IgnoreSize: true}, {Name: "buildbox", IgnoreSize: true},
			}},
		},
		{
			"ignore-size no and yes",
			"host a\n  ignore-size no\nhost b\n\tignore-size yes\n",
			Config{Servers: wantDefault(), Hosts: []Host{{Name: "a"}, {Name: "b", IgnoreSize: true}}},
		},
		{
			"folders repeat, under their own host only",
			"host buildbox\n  ignore-size no\n  folder docs-site code/team/docs-website\n  folder ops code/ops\nhost server1\n",
			Config{Servers: wantDefault(), Hosts: []Host{
				{Name: "buildbox", Folders: map[string]string{
					"docs-site": "code/team/docs-website",
					"ops":       "code/ops",
				}},
				{Name: "server1", IgnoreSize: true},
			}},
		},
		{
			"comments: indented, trailing, and a # inside a word stays",
			"servers tmx # just the one\nhost a  # the first\n  # about a\n  folder c#x code/c#x\n",
			Config{Servers: []string{"tmx"}, Hosts: []Host{
				{Name: "a", IgnoreSize: true, Folders: map[string]string{"c#x": "code/c#x"}},
			}},
		},
		{
			"servers may come after hosts",
			"host a\nservers tmx\n",
			Config{Servers: []string{"tmx"}, Hosts: []Host{{Name: "a", IgnoreSize: true}}},
		},
		{
			"blank lines inside a host block keep the block",
			"host a\n\n  ignore-size no\n",
			Config{Servers: wantDefault(), Hosts: []Host{{Name: "a"}}},
		},
		{
			"strip-prefix and strip-suffix, at the top level",
			"strip-prefix wip- lab1_\nstrip-suffix -run -demo\nhost a\n",
			Config{Servers: wantDefault(), Strip: Strip{Prefixes: []string{"wip-", "lab1_"}, Suffixes: []string{"-run", "-demo"}},
				Hosts: []Host{{Name: "a", IgnoreSize: true}}},
		},
		{
			"strip lines may come after hosts and end their block",
			"host a\n  ignore-size no\nstrip-suffix _tmp\n",
			Config{Servers: wantDefault(), Strip: Strip{Suffixes: []string{"_tmp"}}, Hosts: []Host{{Name: "a"}}},
		},
		{
			"repos, under their own host only; no line means the default",
			"host a\n  repos src work/projects .\nhost b\n",
			Config{Servers: wantDefault(), Hosts: []Host{
				{Name: "a", IgnoreSize: true, Repos: []string{"src", "work/projects", "."}},
				{Name: "b", IgnoreSize: true},
			}},
		},
		{
			"CRLF line ends",
			"host a\r\n  ignore-size no\r\n",
			Config{Servers: wantDefault(), Hosts: []Host{{Name: "a"}}},
		},
	} {
		got, err := parseConfig(strings.NewReader(tc.in), "tmx.conf")
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}
}

func TestParseConfigErrors(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"hots server1\n", "tmx.conf:1: unknown keyword hots"},
		{"host a\n  colour red\n", "tmx.conf:2: unknown keyword colour"},
		{"  ignore-size no\n", "tmx.conf:1: indented line with no host above it"},
		{"# top\n\n  folder x y\n", "tmx.conf:3: indented line with no host above it"},
		{"host a\nservers tmx\n  ignore-size no\n", "tmx.conf:3: indented line with no host above it"},
		{"host a\nhost b\nhost a\n", "tmx.conf:3: duplicate host a"},
		{"host Server1\nhost server1\n", "tmx.conf:2: host server1 duplicates Server1 (names differing only in case)"},
		{"host\n", "tmx.conf:1: host takes one name, got 0"},
		{"host a b\n", "tmx.conf:1: host takes one name, got 2"},
		{"host -oProxyCommand=x\n", "tmx.conf:1: host -oProxyCommand=x is not a plain ssh host name"},
		{"host *.lan\n", "tmx.conf:1: host *.lan is not a plain ssh host name"},
		{"host a\n  host b\n", "tmx.conf:2: host must not be indented"},
		{"host a\n  servers tmx\n", "tmx.conf:2: servers must not be indented"},
		{"ignore-size no\n", "tmx.conf:1: ignore-size belongs to a host: indent it under a host line"},
		{"folder x y\n", "tmx.conf:1: folder belongs to a host: indent it under a host line"},
		{"host a\n  ignore-size maybe\n", "tmx.conf:2: ignore-size takes yes or no"},
		{"host a\n  ignore-size\n", "tmx.conf:2: ignore-size takes yes or no"},
		{"host a\n  ignore-size yes no\n", "tmx.conf:2: ignore-size takes yes or no"},
		{"host a\n  ignore-size no\n  ignore-size yes\n", "tmx.conf:3: ignore-size given twice for a"},
		{"host a\n  folder x\n", "tmx.conf:2: folder takes KEYWORD PATH"},
		{"host a\n  folder x a b\n", "tmx.conf:2: folder takes KEYWORD PATH"},
		{"host a\n  folder x /abs\n", "tmx.conf:2: folder path /abs is relative to home; drop the leading /"},
		{"host a\n  folder x ~/code\n", "tmx.conf:2: folder path ~/code is relative to home; drop the leading ~"},
		{"host a\n  folder x code/a\n  folder x code/b\n", "tmx.conf:3: folder x given twice for a"},
		{"servers\n", "tmx.conf:1: servers needs at least one server name"},
		{"servers tmx\nservers default\n", "tmx.conf:2: servers given twice"},
		{"servers tmx default tmx\n", "tmx.conf:1: server tmx listed twice"},
		{"strip-prefix\n", "tmx.conf:1: strip-prefix needs at least one value"},
		{"strip-suffix\n", "tmx.conf:1: strip-suffix needs at least one value"},
		{"strip-prefix a-\nstrip-prefix b-\n", "tmx.conf:2: strip-prefix given twice"},
		{"strip-suffix -run\nhost a\nstrip-suffix -demo\n", "tmx.conf:3: strip-suffix given twice"},
		{"strip-suffix -run -demo -run\n", "tmx.conf:1: strip-suffix lists -run twice"},
		{"strip-prefix a- a-\n", "tmx.conf:1: strip-prefix lists a- twice"},
		{"host a\n  strip-prefix x\n", "tmx.conf:2: strip-prefix must not be indented"},
		{"host a\n  strip-suffix x\n", "tmx.conf:2: strip-suffix must not be indented"},
		{"repos code\n", "tmx.conf:1: repos belongs to a host: indent it under a host line"},
		{"host a\n  repos\n", "tmx.conf:2: repos needs at least one folder"},
		{"host a\n  repos /src\n", "tmx.conf:2: repos folder /src is relative to home; drop the leading /"},
		{"host a\n  repos src ~/work\n", "tmx.conf:2: repos folder ~/work is relative to home; drop the leading ~"},
		{"host a\n  repos code/../..\n", "tmx.conf:2: repos folder code/../.. must stay under home"},
		{"host a\n  repos src\n  repos work\n", "tmx.conf:3: repos given twice for a"},
		{"host a\n  repos src work src\n", "tmx.conf:2: repos lists src twice"},
	} {
		_, err := parseConfig(strings.NewReader(tc.in), "tmx.conf")
		if err == nil || err.Error() != tc.want {
			t.Errorf("parseConfig(%q) error %v, want %q", tc.in, err, tc.want)
		}
	}
}

// A byte order mark at the start of the file is not part of the first
// keyword; one anywhere else is still an error.
func TestParseConfigBOM(t *testing.T) {
	got, err := parseConfig(strings.NewReader("\ufeffhost server1\n  ignore-size no\n"), "tmx.conf")
	if err != nil || len(got.Hosts) != 1 || got.Hosts[0].Name != "server1" || got.Hosts[0].IgnoreSize {
		t.Errorf("BOM first line: %+v, %v", got, err)
	}
	got, err = parseConfig(strings.NewReader("\ufeff# comment\nhost server1\n"), "tmx.conf")
	if err != nil || len(got.Hosts) != 1 {
		t.Errorf("BOM before a comment: %+v, %v", got, err)
	}
	_, err = parseConfig(strings.NewReader("host a\n\ufeffhost b\n"), "tmx.conf")
	if err == nil || err.Error() != "tmx.conf:2: unknown keyword \ufeffhost" {
		t.Errorf("BOM on line 2: %v", err)
	}
}

// What a config hands back is the caller's to change: the default server
// list must not be one shared slice.
func TestDefaultServersAreNotShared(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "missing"), "x")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Servers[0] = "changed"
	if got, _ := parseConfig(strings.NewReader(""), "x"); !reflect.DeepEqual(got.Servers, wantDefault()) {
		t.Errorf("parseConfig sees %q after a caller edited loadConfig's result", got.Servers)
	}
	if got, _ := loadConfig(filepath.Join(t.TempDir(), "missing"), "x"); !reflect.DeepEqual(got.Servers, wantDefault()) {
		t.Errorf("loadConfig sees %q after a caller edited an earlier result", got.Servers)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()

	// No file: this machine only, default servers, and no error.
	cfg, err := loadConfig(filepath.Join(dir, "missing.conf"), "~/.config/tmx.conf")
	if err != nil || cfg.Found || len(cfg.Hosts) != 0 || !reflect.DeepEqual(cfg.Servers, wantDefault()) {
		t.Errorf("missing file: %+v, %v", cfg, err)
	}

	p := filepath.Join(dir, "tmx.conf")
	if err := os.WriteFile(p, []byte("host server1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(p, "shown")
	if err != nil || !cfg.Found || len(cfg.Hosts) != 1 {
		t.Errorf("present file: %+v, %v", cfg, err)
	}

	// Errors carry the path as shown, not the real one.
	if err := os.WriteFile(p, []byte("host server1\nbogus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadConfig(p, "~/.config/tmx.conf"); err == nil || err.Error() != "~/.config/tmx.conf:2: unknown keyword bogus" {
		t.Errorf("error %v", err)
	}

	// A folder in place of the file is an error, not "no config".
	if _, err = loadConfig(dir, "shown"); err == nil {
		t.Error("a directory read as a config")
	}
}

func TestHostListPutsThisMachineFirst(t *testing.T) {
	names := func(hs []Host) string {
		var s []string
		for _, h := range hs {
			s = append(s, h.Name)
		}
		return strings.Join(s, " ")
	}
	cfgHosts := []Host{{Name: "server1", IgnoreSize: true}, {Name: "laptop"}, {Name: "buildbox"}}
	for _, tc := range []struct {
		self   string
		hosts  []Host
		want   string
		ignore bool // this machine's IgnoreSize
	}{
		{"laptop", nil, "laptop", true},
		{"elsewhere", cfgHosts, "elsewhere server1 laptop buildbox", true},
		// A host line for this machine adds no second row, and its
		// settings apply to this machine.
		{"laptop", cfgHosts, "laptop server1 buildbox", false},
		// A hostname's case may differ from the config's; the row keeps
		// the machine's own spelling.
		{"Laptop", cfgHosts, "Laptop server1 buildbox", false},
		{"server1", cfgHosts, "server1 laptop buildbox", true},
	} {
		got := hostList(tc.self, tc.hosts)
		if names(got) != tc.want || got[0].IgnoreSize != tc.ignore {
			t.Errorf("hostList(%q) = %q ignore-size %v, want %q %v", tc.self, names(got), got[0].IgnoreSize, tc.want, tc.ignore)
		}
	}

	// Folders and repos on a self line reach this machine's row.
	got := hostList("buildbox", []Host{{Name: "buildbox", Folders: map[string]string{"k": "code/k"}, Repos: []string{"src"}}})
	if got[0].Folders["k"] != "code/k" || !reflect.DeepEqual(got[0].repoDirs(), []string{"src"}) {
		t.Errorf("self folders or repos lost: %+v", got)
	}
	// With no self line, this machine searches the default folders.
	if got := hostList("buildbox", nil); !reflect.DeepEqual(got[0].repoDirs(), []string{"code"}) {
		t.Errorf("default repos for this machine: %q", got[0].repoDirs())
	}
}

func TestRepoDirs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		repos []string
		want  []string
	}{
		{"no repos line: code", nil, []string{"code"}},
		{"one folder", []string{"src"}, []string{"src"}},
		{"several, in order", []string{"work", "src/team", "."}, []string{"work", "src/team", "."}},
	} {
		if got := (Host{Repos: tc.repos}).repoDirs(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: repoDirs = %q, want %q", tc.name, got, tc.want)
		}
	}
	// The default is a fresh slice, so a caller editing it can't change it
	// for the next host.
	d := Host{}.repoDirs()
	d[0] = "changed"
	if got := (Host{}).repoDirs(); got[0] != "code" {
		t.Errorf("default repos shared: %q", got)
	}
}

// The example config is documentation; it must stay valid, and so must
// its commented-out samples once their # is taken off.
func TestExampleConfigParses(t *testing.T) {
	cfg, err := loadConfig("tmx.conf.example", "tmx.conf.example")
	if err != nil || !cfg.Found || len(cfg.Hosts) == 0 {
		t.Errorf("%+v, %v", cfg, err)
	}
	b, err := os.ReadFile("tmx.conf.example")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for line := range strings.SplitSeq(string(b), "\n") {
		// A sample is a # with a keyword right after it, or after the
		// indent of a host block.
		rest, ok := strings.CutPrefix(line, "#")
		if ok && rest != "" && (rest[0] != ' ' || strings.HasPrefix(rest, "  ") && rest[2] != ' ') {
			line = rest
		}
		lines = append(lines, line)
	}
	cfg, err = parseConfig(strings.NewReader(strings.Join(lines, "\n")), "tmx.conf.example, uncommented")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Strip.Prefixes == nil || cfg.Strip.Suffixes == nil {
		t.Errorf("the strip samples were not read: %+v", cfg.Strip)
	}
	if last := cfg.Hosts[len(cfg.Hosts)-1]; last.Repos == nil {
		t.Errorf("the repos sample under the last host was not read: %+v", last)
	}
}
