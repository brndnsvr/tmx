package main

import (
	"os"
	"runtime/debug"
	"strings"
	"testing"
)

// The tests' own naming rules, as a config's strip-prefix and
// strip-suffix lines would set them.
var testStrip = Strip{Prefixes: []string{"wip-", "lab1_"}, Suffixes: []string{"-run", "-demo"}}

func TestBaseName(t *testing.T) {
	for _, tc := range []struct {
		in    string
		st    Strip
		want  string
		label string
	}{
		// Defaults: nothing but a trailing -<digits> comes off.
		{"webapp", Strip{}, "webapp", "plain"},
		{"infra-23", Strip{}, "infra", "digits"},
		{"api-server-20261124", Strip{}, "api-server", "a date"},
		{"x-9-8", Strip{}, "x", "repeated digits"},
		{"webapp-run", Strip{}, "webapp-run", "no suffix by default"},
		{"webapp-run-3", Strip{}, "webapp-run", "digits only by default"},
		{"wip-webapp", Strip{}, "wip-webapp", "no prefix by default"},
		{"-12", Strip{}, "", "nothing left"},
		// Configured prefixes and suffixes.
		{"webapp-run", testStrip, "webapp", "suffix"},
		{"infra-23", testStrip, "infra", "digits still"},
		{"api-server-20261124", testStrip, "api-server", "date still"},
		{"lab1_infra-001", testStrip, "infra", "second prefix"},
		{"docs-site-2", testStrip, "docs-site", "dash inside kept"},
		{"wip-webapp-demo-3-run", testStrip, "webapp", "prefix, then a mixed run"},
		{"wip-lab1_x", testStrip, "lab1_x", "only one prefix comes off"},
		{"plain", testStrip, "plain", "nothing to strip"},
		{"run", testStrip, "run", "a suffix needs its dash"},
		{"v2", testStrip, "v2", "digits need their dash"},
		{"name-", testStrip, "name-", "a bare dash is not -<digits>"},
		{"a-b2", testStrip, "a-b2", "mixed text is not -<digits>"},
		{"x-run-demo-9", testStrip, "x", "repeated, in any order"},
		{"wip-", testStrip, "", "prefix only"},
		{"-12", testStrip, "", "digits only"},
		// A suffix need not start with a dash, and the first prefix in
		// the list that matches is the one taken.
		{"notes_tmp", Strip{Suffixes: []string{"_tmp"}}, "notes", "suffix without a dash"},
		{"ab-x", Strip{Prefixes: []string{"a", "ab-"}}, "b-x", "list order, not longest"},
		{"ab-x", Strip{Prefixes: []string{"ab-", "a"}}, "x", "list order"},
	} {
		if got := baseName(tc.in, tc.st); got != tc.want {
			t.Errorf("%s: baseName(%q, %+v) = %q, want %q", tc.label, tc.in, tc.st, got, tc.want)
		}
	}
}

func TestPickFolder(t *testing.T) {
	const home = "/home/u"
	folders := map[string]string{
		"docs-site": "code/team/docs-website",
		"docs":      "code/docs",
		"zz-site":   "code/zz", // ties with aa-site on length
		"aa-site":   "code/aa",
	}
	sessions := []Session{
		{Name: "webapp-001", Path: "/home/u/code/team/web/webapp", Activity: 100},
		{Name: "webapp-002", Path: "/home/u/old", Activity: 50},
		{Name: "wip-webapp", Path: "/home/u/tie-b", Activity: 100},
		{Name: "api-server-1", Path: "", Activity: 900}, // no path: never a sibling
		// A sibling the folder map must win over.
		{Name: "docs-site-1", Path: "/home/u/elsewhere", Activity: 99},
		{Name: "infra-1", Path: "/home/u/a", Activity: 7},
		{Name: "infra-2", Path: "/home/u/b", Activity: 7},
	}
	repos := []string{
		"/home/u/code/api-server",
		"/home/u/code/x/y/api-server",
		"/home/u/code/_archive/tool",
		"/home/u/code/OLD_tool/tool",
		"/home/u/code/a/.trash/tool",
		"/home/u/code/n/node_modules/tool",
		"/home/u/code/w/.worktrees/tool",
		"/home/u/code/bb/lib",
		"/home/u/code/aa/lib",
		"/home/u/code/ops",
		"/home/u/code/api",
		"/home/u/code/server",
		"/home/u/code/deep/er/server",
		"/home/u/code/_archive/zed",
		"/home/u/code/zz/pre",
		"/home/u/code/y/pre",
	}
	for _, tc := range []struct {
		name, dir, why string
		folders        map[string]string
	}{
		// 1. Folder map, longest keyword first, ties alphabetical.
		{"docs-site-2", "/home/u/code/team/docs-website", "folder map docs-site", folders},
		{"docs-sync", "/home/u/code/docs", "folder map docs", folders},
		{"aa-site-and-zz-site", "/home/u/code/aa", "folder map aa-site", folders},
		// The map beats a sibling.
		{"wip-docs-site", "/home/u/code/team/docs-website", "folder map docs-site", folders},
		// 2. Sibling, most recently active; a tie goes to the first name.
		{"webapp-run", "/home/u/code/team/web/webapp", "sibling webapp-001", nil},
		{"infra-9", "/home/u/a", "sibling infra-1", nil},
		// 3. Repo, shortest path; the pathless session doesn't count.
		{"api-server-20261124", "/home/u/code/api-server", "repo", nil},
		// Equal lengths go alphabetically.
		{"lib-run", "/home/u/code/aa/lib", "repo", nil},
		// A repo whose name is a whole run of segments of the base name;
		// an exact repo name still comes first.
		{"api-server-test9", "/home/u/code/api-server", "repo api-server", nil},
		{"ops-tools", "/home/u/code/ops", "repo ops", nil},
		{"tools-ops", "/home/u/code/ops", "repo ops", nil},
		{"team-ops-lab", "/home/u/code/ops", "repo ops", nil},
		{"tools_ops-lab", "/home/u/code/ops", "repo ops", nil},
		// Longest name wins, api-server over api.
		{"api-server-x", "/home/u/code/api-server", "repo api-server", nil},
		{"api-x", "/home/u/code/api", "repo api", nil},
		// Equal names: the shortest path, then alphabetical.
		{"my_server-x", "/home/u/code/server", "repo server", nil},
		{"pre-a", "/home/u/code/y/pre", "repo pre", nil},
		// A short name must not match in the middle of a word.
		{"netops", home, "home", nil},
		{"netops-tools", home, "home", nil},
		{"tools-opsx", home, "home", nil},
		{"develops-x", home, "home", nil},
		{"rapid-x", home, "home", nil},
		// A skipped repo is no match for a part of the name either.
		{"zed-x", home, "home", nil},
		// Every skipped repo leaves nothing.
		{"tool", home, "home", nil},
		// 4. Home.
		{"nothing-here", home, "home", nil},
		{"wip-", home, "home", nil},
	} {
		got := pickFolder(tc.name, home, tc.folders, sessions, repos, testStrip, Folder{home, "home"})
		if got.Dir != tc.dir || got.Why != tc.why {
			t.Errorf("pickFolder(%q) = %+v, want {%s %s}", tc.name, got, tc.dir, tc.why)
		}
	}
	// A session never counts as its own sibling.
	got := pickFolder("webapp-001", home, nil, sessions[:1], nil, testStrip, Folder{home, "home"})
	if got.Why != "home" {
		t.Errorf("own session used as sibling: %+v", got)
	}
	// Without strip-suffix, -run is part of the name, so webapp-run has
	// no sibling and no repo.
	got = pickFolder("webapp-run", home, nil, sessions, repos, Strip{}, Folder{home, "home"})
	if got.Why != "home" {
		t.Errorf("default strip still took -run off: %+v", got)
	}
}

// The fallback is the caller's: home for another host, the folder tmx was
// run from for this one. It is used only when no rule applies, and is
// returned exactly as given.
func TestPickFolderFallback(t *testing.T) {
	const home = "/home/u"
	here := Folder{"/work/here", "here"}
	sessions := []Session{{Name: "webapp-001", Path: "/home/u/code/webapp", Activity: 1}}
	repos := []string{"/home/u/code/api-server", "/home/u/code/_archive/tool"}
	folders := map[string]string{"docs-site": "code/docs-website"}
	for _, tc := range []struct {
		name     string
		fallback Folder
		want     Folder
	}{
		{"nothing-here", here, here},
		{"nothing-here", Folder{home, "home"}, Folder{home, "home"}},
		{"wip-", here, here},     // an empty base name
		{"tool-3", here, here},   // only a skipped repo matches
		{"netops", here, here},   // no whole-segment match
		{"", Folder{}, Folder{}}, // returned as given, even empty
		// Every rule still beats the fallback.
		{"docs-site-9", here, Folder{"/home/u/code/docs-website", "folder map docs-site"}},
		{"webapp-run", here, Folder{"/home/u/code/webapp", "sibling webapp-001"}},
		{"api-server-2", here, Folder{"/home/u/code/api-server", "repo"}},
		{"api-server-test9", here, Folder{"/home/u/code/api-server", "repo api-server"}},
	} {
		if got := pickFolder(tc.name, home, folders, sessions, repos, testStrip, tc.fallback); got != tc.want {
			t.Errorf("pickFolder(%q, fallback %+v) = %+v, want %+v", tc.name, tc.fallback, got, tc.want)
		}
	}
}

func TestShortHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"laptop.local", "laptop"},
		{"server1", "server1"},
		{"a.b.c", "a"},
		{"H7XR2M4KQ9", "H7XR2M4KQ9"}, // a serial-number hostname stays as it is
		{"", ""},
	} {
		if got := shortHost(tc.in); got != tc.want {
			t.Errorf("shortHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestViaShQuotingRoundTrips(t *testing.T) {
	for _, in := range []string{"echo hi", "it's", `a "b" $c ~ d:e`, "x\ny", "''", `\`} {
		got := shellSplit(t, viaSh(in))
		if len(got) != 3 || got[0] != "sh" || got[1] != "-c" || got[2] != in {
			t.Errorf("viaSh(%q) reads back as %q", in, got)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		rc      int
		timeout bool
		err     string
		want    string
	}{
		{0, false, "", ""},
		{0, false, "noise on stderr", ""},
		{3, false, "", "no tmux"},
		{0, true, "", "timed out"},
		{255, true, "Permission denied", "timed out"},
		{255, false, "Host key verification failed.", "host key mismatch"},
		{255, false, "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@", "host key mismatch"},
		{255, false, "u@h: Permission denied (publickey).", "auth refused"},
		{255, false, "Received disconnect: Too many authentication failures", "auth refused"},
		{255, false, "ssh: Could not resolve hostname x", "unreachable"},
		{255, false, "ssh: connect to host x port 22: Operation timed out", "unreachable"},
		{255, false, "connect to host x port 22: No route to host", "unreachable"},
		{255, false, "Network is unreachable", "unreachable"},
		{255, false, "connect to host x port 22: Connection refused", "unreachable"},
		{255, false, "Connection closed by 1.2.3.4 port 22", "unreachable"},
		{255, false, "Connection reset by peer", "unreachable"},
		{255, false, "something else", "ssh failed"},
		{1, false, "", "failed (rc 1)"},
		{127, false, "", "failed (rc 127)"},
	} {
		if got := classify(tc.rc, tc.timeout, tc.err); got != tc.want {
			t.Errorf("classify(%d, %v, %q) = %q, want %q", tc.rc, tc.timeout, tc.err, got, tc.want)
		}
	}
}

func TestParseLine(t *testing.T) {
	servers := []string{"default", "tmx"}
	for _, tc := range []struct {
		line string
		ok   bool
		want entry
	}{
		{"1:3:0:$5:work", true, entry{server: "default", windows: "3", attached: "0", id: "$5", name: "work"}},
		{"2:1:2:$12:a: b:c", true, entry{server: "tmx", windows: "1", attached: "2", id: "$12", name: "a: b:c"}},
		{"2:1:0:$0:", true, entry{server: "tmx", windows: "1", attached: "0", id: "$0", name: ""}},
		{"0:1:0:$1:x", false, entry{}},
		{"3:1:0:$1:x", false, entry{}},
		{"0001:1:0:$1:x", false, entry{}},
		{"99999999999999999999:1:0:$1:x", false, entry{}},
		{"1:x:0:$1:x", false, entry{}},
		{"1:1::$1:x", false, entry{}},
		{"1:1:0:1:x", false, entry{}},
		{"1:1:0:$:x", false, entry{}},
		{"1:1:0:$1a:x", false, entry{}},
		{"1:1:0:$1", false, entry{}},
		{"", false, entry{}},
		{"garbage", false, entry{}},
		{"-1:1:0:$1:x", false, entry{}},
	} {
		got, ok := parseLine(tc.line, servers)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseLine(%q) = %+v, %v; want %+v, %v", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSanitize(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain name", "plain name"},
		{"tab\there", "tab?here"},
		{"esc\x1b[31mred", "esc?[31mred"},
		{"nul\x00", "nul?"},
		{"del\x7f", "del?"},
		{"café ✓", "café ✓"},
		{"bad\xffbyte", "bad?byte"},
		{"bidi\u202eflip", "bidi?flip"},
		{"c1\u0085", "c1?"},
	} {
		if got := sanitize(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLayoutWidths(t *testing.T) {
	long := strings.Repeat("n", 50)
	for _, tc := range []struct {
		name   string
		es     []entry
		rows   []string
		header string
	}{
		{
			"never narrower than the header",
			[]entry{{host: "a", server: "b", name: "c", windows: "1", attached: "0", id: "$1"}},
			[]string{"a     b       c          1 win  0 attached"},
			"host  server  session  windows  attached",
		},
		{
			"fits the widest cell",
			[]entry{
				{host: "web01", server: "default", name: "work", windows: "12", attached: "1", id: "$1"},
				{host: "lab", server: "tmx", name: "a much longer name", windows: "3", attached: "0", id: "$2"},
			},
			[]string{
				"web01  default  work                 12 win  1 attached",
				"lab    tmx      a much longer name    3 win  0 attached",
			},
			"host   server   session             windows  attached",
		},
		{
			"status rows: host then reason; reason not measured",
			[]entry{
				{host: "jump1", status: "a very long reason that is not a column"},
				{host: "lab", server: "tmx", name: "s", windows: "1", attached: "0", id: "$1"},
			},
			[]string{
				"jump1  a very long reason that is not a column",
				"lab    tmx     s          1 win  0 attached",
			},
			"host   server  session  windows  attached",
		},
		{
			"capped 20/20/40/6",
			[]entry{{host: long, server: long, name: long, windows: "123456789", attached: "1", id: "$1"}},
			[]string{long + "  " + long + "  " + long + "  123456789 win  1 attached"},
			strings.Join([]string{pad("host", 20), pad("server", 20), pad("session", 40), pad("windows", 10), "attached"}, "  "),
		},
		{
			"measured after sanitizing, in characters",
			[]entry{{host: "h", server: "s", name: "é\x1b", windows: "1", attached: "0", id: "$1"}},
			[]string{"h     s       é?         1 win  0 attached"},
			"host  server  session  windows  attached",
		},
	} {
		rows, header := layout(tc.es)
		if header != tc.header {
			t.Errorf("%s: header\n got %q\nwant %q", tc.name, header, tc.header)
		}
		if strings.Join(rows, "\n") != strings.Join(tc.rows, "\n") {
			t.Errorf("%s: rows\n got %q\nwant %q", tc.name, rows, tc.rows)
		}
	}
}

func pad(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }

func TestQuoting(t *testing.T) {
	for _, tc := range []struct{ in, min, always string }{
		{"work", "work", "'work'"},
		{"/a/b-c_d.e", "/a/b-c_d.e", "'/a/b-c_d.e'"},
		{"", "''", "''"},
		{"a b", "'a b'", "'a b'"},
		{"it's", `'it'\''s'`, `'it'\''s'`},
		{"$1", "'$1'", "'$1'"},
	} {
		if got := qMin(tc.in); got != tc.min {
			t.Errorf("qMin(%q) = %q, want %q", tc.in, got, tc.min)
		}
		if got := q(tc.in); got != tc.always {
			t.Errorf("q(%q) = %q, want %q", tc.in, got, tc.always)
		}
		if got := shellSplit(t, q(tc.in)); len(got) != 1 || got[0] != tc.in {
			t.Errorf("q(%q) does not round-trip: %q", tc.in, got)
		}
	}
}

func TestTmuxPrefix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/tmp/tmux-501/tmx,123,0", "/tmp/tmux-501/tmx"},
		{"/tmp/a,b/tmx,1,0", "/tmp/a,b/tmx"},
		{"/tmp/x,1", "/tmp/x,1"},
		{"/tmp/x", "/tmp/x"},
	} {
		if got := tmuxPrefix(tc.in); got != tc.want {
			t.Errorf("tmuxPrefix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseArgs(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want options
		bad  bool
	}{
		{nil, options{}, false},
		{[]string{"-l"}, options{local: true}, false},
		{[]string{"--local"}, options{local: true}, false},
		{[]string{"-r", "server1"}, options{remote: "server1"}, false},
		{[]string{"--remote=server1"}, options{remote: "server1"}, false},
		{[]string{"work"}, options{name: "work", named: true}, false},
		{[]string{"-r", "buildbox", "x"}, options{remote: "buildbox", name: "x", named: true}, false},
		{[]string{"x", "--dir", "/d"}, options{name: "x", named: true, dir: "/d", hasDir: true}, false},
		{[]string{"--dir=/d", "x"}, options{name: "x", named: true, dir: "/d", hasDir: true}, false},
		{[]string{"--", "-odd"}, options{name: "-odd", named: true}, false},
		{[]string{"-h"}, options{help: true}, false},
		{[]string{"--help"}, options{help: true}, false},
		{[]string{"x", "-h"}, options{help: true}, false},
		{[]string{"--version"}, options{version: true}, false},
		{[]string{"--hosts"}, options{hosts: true}, false},
		{[]string{"-r"}, options{}, true},
		{[]string{"--dir", "/d"}, options{}, true},
		{[]string{"-l", "-r", "laptop"}, options{}, true},
		{[]string{"-l", "x"}, options{}, true},
		{[]string{"a", "b"}, options{}, true},
		{[]string{"-x"}, options{}, true},
		{[]string{""}, options{}, true},
		{[]string{"x", "--dir="}, options{}, true},
		{[]string{"--version", "x"}, options{}, true},
		{[]string{"-l", "--hosts"}, options{}, true},
		{[]string{"--hosts", "--version"}, options{}, true},
		{[]string{"--", "--version"}, options{name: "--version", named: true}, false},
		{[]string{"doctor"}, options{doctor: true}, false},
		{[]string{"--", "doctor"}, options{name: "doctor", named: true}, false},
		{[]string{"doctor", "x"}, options{}, true},
		{[]string{"-l", "doctor"}, options{}, true},
		{[]string{"-r", "server1", "doctor"}, options{}, true},
		{[]string{"doctor", "--hosts"}, options{}, true},
	} {
		got, err := parseArgs(tc.args)
		if tc.bad {
			if err == nil {
				t.Errorf("parseArgs(%q) = %+v, want an error", tc.args, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseArgs(%q) = %+v, %v; want %+v", tc.args, got, err, tc.want)
		}
	}
}

// The error names the flag that must go alone, not whatever came first.
func TestAloneErrorNamesTheFlagAtFault(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-l", "--version"}, "--version goes alone"},
		{[]string{"x", "--hosts"}, "--hosts goes alone"},
		{[]string{"--version", "x"}, "--version goes alone"},
		{[]string{"-r", "laptop", "--hosts"}, "--hosts goes alone"},
		{[]string{"--hosts", "--version"}, "--hosts goes alone"},
		{[]string{"-r", "server1", "doctor"}, "doctor goes alone"},
		{[]string{"doctor", "--version"}, "doctor goes alone"},
	} {
		_, err := parseArgs(tc.args)
		if err == nil || err.Error() != tc.want {
			t.Errorf("parseArgs(%q) error %v, want %q", tc.args, err, tc.want)
		}
	}
}

func TestExpandTilde(t *testing.T) {
	for _, tc := range []struct{ dir, home, want string }{
		{"~", "/h", "/h"},
		{"~/", "/h", "/h"},
		{"~/a b", "/h", "/h/a b"},
		{"~bob/a", "/h", "~bob/a"},
		{"a/~/b", "/h", "a/~/b"},
		{"/x", "/h", "/x"},
		{"~/a", "", "~/a"},
	} {
		if got := expandTilde(tc.dir, tc.home); got != tc.want {
			t.Errorf("expandTilde(%q, %q) = %q, want %q", tc.dir, tc.home, got, tc.want)
		}
	}
}

// A failure to start the final command is an ordinary failure, 1, never a
// shell-style 126 or 127.
func TestFinishFailingToStartIsOne(t *testing.T) {
	var errb strings.Builder
	a := &app{stderr: &errb}
	if rc := a.finish([]string{"/nonexistent/tmx-test-binary"}); rc != 1 {
		t.Errorf("rc %d, stderr %q", rc, errb.String())
	}
}

func TestParseGather(t *testing.T) {
	out := strings.Join([]string{
		"H:/home/u",
		"S:$1:100:webapp-001",
		"S:$2:200:a: b",
		"S:bad:1:x",
		"P:$1:/home/u/code/webapp",
		"P:$2:/home/u/with: colon",
		"P:$9:relative",
		"R:/home/u/code/x/.git",
		"R:/home/u/code/y/.gitmodules",
		"junk",
	}, "\n")
	g := parseGather(out, "a: b")
	if g.home != "/home/u" || !g.exists {
		t.Fatalf("home %q exists %v", g.home, g.exists)
	}
	want := []Session{
		{Name: "webapp-001", Path: "/home/u/code/webapp", Activity: 100},
		{Name: "a: b", Path: "/home/u/with: colon", Activity: 200},
	}
	if len(g.sessions) != 2 || g.sessions[0] != want[0] || g.sessions[1] != want[1] {
		t.Errorf("sessions %+v", g.sessions)
	}
	if len(g.repos) != 1 || g.repos[0] != "/home/u/code/x" {
		t.Errorf("repos %q", g.repos)
	}
	if parseGather(out, "other").exists {
		t.Error("exists for a missing name")
	}
}

// Each repos folder becomes one quoted find root under $HOME, so a name
// with a quote or a space reaches find as one word.
func TestGatherScriptSearchesTheRepoDirs(t *testing.T) {
	for _, tc := range []struct {
		dirs []string
		want string
	}{
		{[]string{"code"}, `find "$HOME"/'code' -maxdepth 4 -name .git`},
		{[]string{"src", "work/team"}, `find "$HOME"/'src' "$HOME"/'work/team' -maxdepth 4 -name .git`},
		{[]string{"it's"}, `find "$HOME"/'it'\''s' -maxdepth 4 -name .git`},
	} {
		s := gatherScript("tmx", "n", tc.dirs)
		if !strings.Contains(s, tc.want) {
			t.Errorf("gatherScript(%q) lacks %s:\n%s", tc.dirs, tc.want, s)
		}
		// Home's direct children are always checked.
		if !strings.Contains(s, `for g in "$HOME"/*/.git`) {
			t.Errorf("gatherScript(%q) skips home's children", tc.dirs)
		}
	}
}

func TestListJobsNeverStartAMuxMaster(t *testing.T) {
	a := &app{self: "me", ssh: "ssh"}
	argv := a.hostCommand("server1", "SCRIPT")
	joined := strings.Join(argv, " ")
	for _, want := range []string{"ConnectTimeout=2", "BatchMode=yes", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "ControlMaster=no"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh job lacks %s: %q", want, argv)
		}
	}
	if argv[len(argv)-2] != "server1" || argv[len(argv)-1] != "sh -c 'SCRIPT'" {
		t.Errorf("host and sh-wrapped script not last: %q", argv)
	}
	if got := a.hostCommand("me", "SCRIPT"); strings.Join(got, " ") != "sh -c SCRIPT" {
		t.Errorf("self job %q", got)
	}
}

// shellSplit reads back the quoting this program writes: bare words,
// single quotes and backslash escapes.
func shellSplit(t *testing.T, s string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	in, inWord := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case in && c == '\'':
			in = false
		case in:
			cur.WriteByte(c)
		case c == '\'':
			in, inWord = true, true
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
			inWord = true
		case c == ' ' || c == '\n' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if in {
		t.Fatalf("unterminated quote in %q", s)
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f.Fd()) {
		t.Error("/dev/null counted as a terminal, so the picker would wait on fzf")
	}
}

func TestShownVersion(t *testing.T) {
	bi := func(v string) *debug.BuildInfo { return &debug.BuildInfo{Main: debug.Module{Version: v}} }
	for _, tc := range []struct {
		stamp string
		info  *debug.BuildInfo
		want  string
	}{
		{"01966cb", bi("v0.0.0-20261127161053-01966cbea2fa"), "01966cb"},
		{"dev", bi("v0.0.0-20261127161053-01966cbea2fa"), "01966cb"},
		{"dev", bi("v1.2.0"), "v1.2.0"},
		{"dev", bi("(devel)"), "dev"},
		{"dev", bi(""), "dev"},
		{"dev", nil, "dev"},
	} {
		if got := shownVersion(tc.stamp, tc.info); got != tc.want {
			t.Errorf("shownVersion(%q, %v) = %q, want %q", tc.stamp, tc.info, got, tc.want)
		}
	}
}
