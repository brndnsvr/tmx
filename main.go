// tmx creates or rejoins named tmux sessions on the `tmx` socket, and
// picks a session from every tmux server on this machine and the hosts in
// ~/.config/tmx.conf to attach to. Attaching never detaches other clients,
// and the picker never creates or kills a session. All tmux options live
// in ~/.tmux.conf, not here.
package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const usage = `usage: tmx                      pick a session on any host
       tmx -l | --local         pick a session on this machine
       tmx -r | --remote HOST   pick a session on HOST
       tmx NAME                 create or rejoin NAME on this machine
       tmx -r HOST NAME         create or rejoin NAME on HOST
       --dir DIR                start a new session in DIR
       tmx doctor               check every host: ssh, key auth, tmux
       tmx --hosts              list this machine and the configured hosts
       tmx --version            print the version
Hosts come from ~/.config/tmx.conf (TMX_CONF overrides); each one also
needs a ~/.ssh/config entry with key auth. tmx -- doctor names a session.
A new session starts in a folder guessed from its name (folder map,
sibling session, git repo), else in this folder here or home on HOST.
TMX_DRYRUN set: print the final command instead of running it.
Exit: 0 done, 1 failed or nothing picked, 2 usage error, bad config or
a host not in the config. Once tmx hands over to tmux or ssh, their exit
code is the one you get.
`

// app holds everything tmx reads from its surroundings, so tests can run
// it in-process against throwaway tmux servers and a fake ssh.
type app struct {
	hosts       []Host // this machine first, then the config's hosts
	servers     []string
	strip       Strip // how a session name becomes its base name
	namedServer string
	self        string
	ssh         string
	dryRun      bool

	confFound bool
	confShown string // the config path as messages show it
	confErr   error  // a config that doesn't parse stops every command
	sshConf   sshConfig
	sshShown  string
	sshErr    error

	tmuxEnv    string // $TMUX
	tmuxTmpdir string // $TMUX_TMPDIR
	pwd        string

	stdin    io.Reader
	stdinTTY bool
	fzf      string // empty: use the numbered list
	stdout   io.Writer
	stderr   io.Writer

	listTimeout   time.Duration
	gatherTimeout time.Duration
	doctorTimeout time.Duration
}

func selfName() string {
	h, _ := os.Hostname()
	return shortHost(h)
}

func newApp() *app {
	a := &app{
		namedServer:   namedServer,
		self:          selfName(),
		ssh:           "ssh",
		tmuxEnv:       os.Getenv("TMUX"),
		tmuxTmpdir:    os.Getenv("TMUX_TMPDIR"),
		stdin:         os.Stdin,
		stdout:        os.Stdout,
		stderr:        os.Stderr,
		listTimeout:   3 * time.Second,
		gatherTimeout: 10 * time.Second,
		doctorTimeout: 5 * time.Second,
	}
	_, a.dryRun = os.LookupEnv("TMX_DRYRUN")
	if s := os.Getenv("TMX_SSH"); s != "" {
		a.ssh = s
	}
	home, _ := os.UserHomeDir()
	conf := filepath.Join(home, ".config", "tmx.conf")
	if p := os.Getenv("TMX_CONF"); p != "" {
		conf = p
	}
	sshConf := filepath.Join(home, ".ssh", "config")
	if p := os.Getenv("TMX_SSH_CONFIG"); p != "" {
		sshConf = p
	}
	a.configure(conf, sshConf, home)
	a.pwd, _ = os.Getwd()
	a.stdinTTY = isTerminal(os.Stdin.Fd())
	if p, err := exec.LookPath("fzf"); err == nil {
		a.fzf = p
	}
	return a
}

// configure reads tmx.conf and the ssh config. A tmx.conf that doesn't
// parse is kept as an error for main to report, so -h and --version
// still work while it is being fixed.
func (a *app) configure(confPath, sshPath, home string) {
	a.confShown = tilde(confPath, home)
	a.sshShown = tilde(sshPath, home)
	cfg, err := loadConfig(confPath, a.confShown)
	a.confErr = err
	a.confFound = cfg.Found
	a.servers = cfg.Servers
	a.strip = cfg.Strip
	a.hosts = hostList(a.self, cfg.Hosts)
	// Relative Include paths are under ~/.ssh, as ssh reads them, even
	// when TMX_SSH_CONFIG points somewhere else.
	var sshErr error
	a.sshConf, sshErr = loadSSHConfig(sshPath, filepath.Join(home, ".ssh"), home)
	a.sshErr = nil
	if sshErr != nil {
		// Name the file as the user knows it, and the plain reason.
		reason := sshErr
		var pe *fs.PathError
		if errors.As(sshErr, &pe) {
			reason = pe.Err
		}
		a.sshErr = fmt.Errorf("can't read %s: %v", a.sshShown, reason)
	}
}

// sshProblem says why ssh can't be used for host, or "" when it can. This
// machine never goes through ssh. An unreadable ssh config is reported as
// that, not as every host missing from it.
func (a *app) sshProblem(host string) string {
	switch {
	case host == a.self:
		return ""
	case a.sshErr != nil:
		return a.sshErr.Error()
	case !a.sshConf.Has(host):
		return "not in " + a.sshShown
	}
	return ""
}

// version is stamped by the Makefile from git describe; a plain go build
// says dev.
var version = "dev"

func main() {
	os.Exit(newApp().main(os.Args[1:]))
}

type options struct {
	help    bool
	version bool
	hosts   bool
	doctor  bool
	local   bool
	remote  string
	dir     string
	hasDir  bool
	name    string
	named   bool
}

func parseArgs(args []string) (options, error) {
	var o options
	value := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		*i++
		return args[*i], nil
	}
	positional := false
	alone := "" // the first flag that answers a question and stops
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var err error
		switch {
		case !positional && arg == "doctor":
			// A session called doctor is still possible: tmx -- doctor.
			o.doctor = true
			if alone == "" {
				alone = arg
			}
		case positional || !strings.HasPrefix(arg, "-") || arg == "-":
			if o.named {
				return o, fmt.Errorf("one session name at most")
			}
			o.name, o.named = arg, true
		case arg == "--":
			positional = true
		case arg == "-h" || arg == "--help":
			return options{help: true}, nil
		case arg == "--version":
			o.version = true
			if alone == "" {
				alone = arg
			}
		case arg == "--hosts":
			o.hosts = true
			if alone == "" {
				alone = arg
			}
		case arg == "-l" || arg == "--local":
			o.local = true
		case arg == "-r" || arg == "--remote":
			o.remote, err = value(&i, arg)
		case strings.HasPrefix(arg, "--remote="):
			o.remote = strings.TrimPrefix(arg, "--remote=")
		case arg == "--dir":
			o.dir, err = value(&i, arg)
			o.hasDir = true
		case strings.HasPrefix(arg, "--dir="):
			o.dir, o.hasDir = strings.TrimPrefix(arg, "--dir="), true
		default:
			return o, fmt.Errorf("unknown option %s", arg)
		}
		if err != nil {
			return o, err
		}
	}
	// --version, --hosts and doctor answer a question and stop, so
	// anything else with them is a mistake rather than something to ignore.
	if alone != "" && len(args) > 1 {
		return o, fmt.Errorf("%s goes alone", alone)
	}
	switch {
	case o.local && o.remote != "":
		return o, fmt.Errorf("-l and -r don't go together")
	case o.local && o.named:
		return o, fmt.Errorf("-l takes no session name; use tmx NAME")
	case o.hasDir && !o.named:
		return o, fmt.Errorf("--dir needs a session name")
	case o.hasDir && o.dir == "":
		return o, fmt.Errorf("--dir needs a folder")
	case o.named && o.name == "":
		return o, fmt.Errorf("empty session name")
	}
	return o, nil
}

func (a *app) host(name string) (Host, bool) {
	for _, h := range a.hosts {
		// This machine answers to its name in any case, as hostList does.
		if h.Name == name || (h.Name == a.self && strings.EqualFold(name, a.self)) {
			return h, true
		}
	}
	return Host{}, false
}

// selfHost is this machine's row, always first in the host list.
func (a *app) selfHost() Host {
	if h, ok := a.host(a.self); ok {
		return h
	}
	return Host{Name: a.self, IgnoreSize: true}
}

func (a *app) hostNames() string {
	names := make([]string, len(a.hosts))
	for i, h := range a.hosts {
		names[i] = h.Name
	}
	return strings.Join(names, " ")
}

func (a *app) main(args []string) int {
	o, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(a.stderr, "tmx: %v\n%s", err, usage)
		return 2
	}

	switch {
	case o.help:
		fmt.Fprint(a.stdout, usage)
		return 0
	case o.version:
		fmt.Fprintf(a.stdout, "tmx %s\n", version)
		return 0
	}
	if a.confErr != nil {
		fmt.Fprintf(a.stderr, "tmx: %v\n", a.confErr)
		return 2
	}

	switch {
	case o.doctor:
		return a.doctor()
	case o.hosts:
		for _, h := range a.hosts {
			fmt.Fprintln(a.stdout, h.Name)
		}
		return 0
	case o.remote != "":
		h, ok := a.host(o.remote)
		if !ok {
			fmt.Fprintf(a.stderr, "tmx: %s is not in %s (hosts: %s)\n", o.remote, a.confShown, a.hostNames())
			return 2
		}
		if why := a.sshProblem(h.Name); why != "" {
			// ssh would guess at a host it has no entry for; never try.
			fmt.Fprintf(a.stderr, "tmx: %s: %s\n", h.Name, why)
			return 1
		}
		switch {
		case !o.named:
			return a.pick([]Host{h})
		case h.Name == a.self:
			return a.localNamed(h, o.name, o.dir)
		default:
			return a.remoteNamed(h, o.name, o.dir)
		}
	case o.named:
		return a.localNamed(a.selfHost(), o.name, o.dir)
	case o.local:
		return a.pick([]Host{a.selfHost()})
	default:
		return a.pick(a.hosts)
	}
}
