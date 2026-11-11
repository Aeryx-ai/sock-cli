package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testUnit = runnerUnit{
	Label:   "ai.aeryx.sock-runner.0123456789abcdef",
	Sock:    "/usr/local/bin/sock",
	Project: "/src/app",
	Runner:  "/data/sock-runner",
	Log:     "/state/listen.log",
	Env:     []string{"PATH=/bin:/usr/bin", "SOCK_RUNNER_AGENT=claude"},
}

// The plist is byte for byte what integrations/runner/sock-runner-service
// writes, so an install by either replaces the other's.
func TestRunnerUnitPlist(t *testing.T) {
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>ai.aeryx.sock-runner.0123456789abcdef</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/sock</string>
		<string>listen</string>
		<string>--as-puppet</string>
		<string>--project</string>
		<string>/src/app</string>
		<string>--exec</string>
		<string>/data/sock-runner</string>
	</array>
	<key>WorkingDirectory</key>
	<string>/src/app</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/bin:/usr/bin</string>
		<key>SOCK_RUNNER_AGENT</key>
		<string>claude</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardErrorPath</key>
	<string>/state/listen.log</string>
</dict>
</plist>
`
	if got := testUnit.render(launchd); got != want {
		t.Fatalf("plist:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunnerUnitSystemd(t *testing.T) {
	want := `[Unit]
Description=sock-runner for /src/app
After=network-online.target

[Service]
ExecStart=/usr/local/bin/sock listen --as-puppet --project /src/app --exec /data/sock-runner
WorkingDirectory=/src/app
Environment=PATH=/bin:/usr/bin
Environment=SOCK_RUNNER_AGENT=claude
Restart=always
RestartSec=5
StandardError=append:/state/listen.log

[Install]
WantedBy=default.target
`
	if got := testUnit.render(systemd); got != want {
		t.Fatalf("unit:\n%s\nwant:\n%s", got, want)
	}
}

// fakeHost is a machine for sock agent: a temp HOME, a PATH holding a fake
// sock, and a command runner that records instead of running launchctl,
// systemctl, loginctl or the runner's configuration check.
type fakeHost struct {
	home, bin, project string
	env                map[string]string
	calls              []string
	checkEnv           []string
	fail               map[string]error
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{home: t.TempDir(), bin: t.TempDir(), project: t.TempDir(), fail: map[string]error{}}
	if err := os.WriteFile(filepath.Join(h.bin, "sock"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.env = map[string]string{"HOME": h.home, "PATH": h.bin, "USER": "guy"}
	t.Setenv("PATH", h.bin) // exec.LookPath reads the process PATH
	return h
}

func (h *fakeHost) app(goos string) *App {
	return &App{
		Version: "1.2.3",
		Getenv:  func(k string) string { return h.env[k] },
		OS:      goos,
		RunCmd: func(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
			call := name + " " + strings.Join(args, " ")
			if name == "/bin/sh" {
				call, h.checkEnv = "runner_config", env
			}
			h.calls = append(h.calls, call)
			if err := h.fail[call]; err != nil {
				return []byte("refused\n"), err
			}
			return nil, nil
		},
	}
}

// id is the unit id sock-runner-service derives: the first 16 hex of the
// SHA-256 of the project's physical path.
func (h *fakeHost) id(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(h.project)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:16]
}

func (h *fakeHost) skillPath() string {
	return filepath.Join(h.home, ".agents", "skills", "sock", "SKILL.md")
}

func TestAgentInstallLaunchd(t *testing.T) {
	h := newFakeHost(t)
	out, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	id := h.id(t)
	label := "ai.aeryx.sock-runner." + id
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	plist := filepath.Join(h.home, "Library", "LaunchAgents", label+".plist")
	want := []string{"runner_config", "launchctl bootout " + domain + "/" + label, "launchctl bootstrap " + domain + " " + plist}
	if strings.Join(h.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(h.calls, "\n"), strings.Join(want, "\n"))
	}

	data := filepath.Join(h.home, ".local", "share", "sock", "runner", "1.2.3")
	for name, mode := range map[string]os.FileMode{"sock-runner": 0o755, "lib.sh": 0o644} {
		fi, err := os.Stat(filepath.Join(data, name))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(data, name))
		if string(got) != runnerFiles[name] || fi.Mode().Perm() != mode {
			t.Fatalf("%s: mode %v, content matches %v", name, fi.Mode().Perm(), string(got) == runnerFiles[name])
		}
	}

	project, _ := filepath.EvalSymlinks(h.project)
	state := filepath.Join(h.home, ".local", "state", "sock-integrations", "runner", id)
	if fi, err := os.Stat(state); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("state dir: %v %v", fi, err)
	}
	u := runnerUnit{
		Label: label, Sock: filepath.Join(h.bin, "sock"), Project: project, Runner: filepath.Join(data, "sock-runner"),
		Log: filepath.Join(state, "listen.log"),
		Env: []string{"PATH=" + h.bin, "SOCK_RUNNER_AGENT=claude", "SOCK_RUNNER_PERMISSION=default", "SOCK_RUNNER_WORKDIR=" + project, "SOCK_RUNNER_STATE=" + state},
	}
	got, err := os.ReadFile(plist)
	if err != nil || string(got) != u.render(launchd) {
		t.Fatalf("plist %v:\n%s", err, got)
	}
	// the runner's own check ran with the settings the unit carries
	if strings.Join(h.checkEnv, " ") != strings.Join(u.Env, " ") {
		t.Fatalf("check env %v, want %v", h.checkEnv, u.Env)
	}
	if skill, err := os.ReadFile(h.skillPath()); err != nil || string(skill) != agentSkillMD {
		t.Fatalf("skill not installed: %v", err)
	}
	for _, want := range []string{label, plist, filepath.Join(state, "listen.log"), "launchctl print " + domain + "/" + label, h.skillPath()} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAgentInstallTakesRunnerSettings(t *testing.T) {
	h := newFakeHost(t)
	h.env["SOCK_RUNNER_AGENT"] = "codex"
	h.env["SOCK_RUNNER_PERMISSION"] = "read-only"
	h.env["SOCK_RUNNER_SESSION"] = "s1"
	h.env["SOCK_RUNNER_MODEL"] = "gpt-5"
	if _, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := strings.Join(h.checkEnv, " ")
	for _, want := range []string{"SOCK_RUNNER_AGENT=codex", "SOCK_RUNNER_PERMISSION=read-only", "SOCK_RUNNER_SESSION=s1", "SOCK_RUNNER_MODEL=gpt-5"} {
		if !strings.Contains(got, want) {
			t.Errorf("env %q lacks %s", got, want)
		}
	}
}

func TestAgentInstallIsIdempotent(t *testing.T) {
	h := newFakeHost(t)
	for range 2 {
		if _, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project); err != nil {
			t.Fatalf("install: %v", err)
		}
	}
	units, _ := os.ReadDir(filepath.Join(h.home, "Library", "LaunchAgents"))
	if len(units) != 1 {
		t.Fatalf("%d files in LaunchAgents, want 1", len(units))
	}
	if len(h.calls) != 6 || strings.Join(h.calls[:3], "\n") != strings.Join(h.calls[3:], "\n") {
		t.Fatalf("second install differs from the first:\n%s", strings.Join(h.calls, "\n"))
	}
}

func TestAgentInstallRefusedConfigWritesNoUnit(t *testing.T) {
	h := newFakeHost(t)
	h.fail["runner_config"] = errors.New("exit status 2")
	_, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err %v, want the runner's refusal", err)
	}
	if _, err := os.Stat(filepath.Join(h.home, "Library", "LaunchAgents")); err == nil {
		t.Fatal("unit directory created after a refused configuration")
	}
}

func TestAgentInstallSystemdEnablesLinger(t *testing.T) {
	h := newFakeHost(t)
	out, _, err := run(t, h.app("linux"), nil, "agent", "install", "--project", h.project)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	label := "sock-runner-" + h.id(t) + ".service"
	want := []string{"runner_config", "systemctl --user daemon-reload", "systemctl --user enable --now " + label, "systemctl --user restart " + label, "loginctl enable-linger guy"}
	if strings.Join(h.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(h.calls, "\n"))
	}
	unit := filepath.Join(h.home, ".config", "systemd", "user", label)
	if b, err := os.ReadFile(unit); err != nil || !strings.HasPrefix(string(b), "[Unit]\n") {
		t.Fatalf("unit %v", err)
	}
	for _, want := range []string{unit, "systemctl --user status " + label, "linger is on for guy", "sock agent uninstall leaves it on"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestAgentInstallSystemdHonorsXDGConfig(t *testing.T) {
	h := newFakeHost(t)
	h.env["XDG_CONFIG_HOME"] = filepath.Join(h.home, "cfg")
	if _, _, err := run(t, h.app("linux"), nil, "agent", "install", "--project", h.project); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.home, "cfg", "systemd", "user", "sock-runner-"+h.id(t)+".service")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInstallLingerFailureNamesTheCommand(t *testing.T) {
	h := newFakeHost(t)
	h.fail["loginctl enable-linger guy"] = errors.New("exit status 1")
	out, _, err := run(t, h.app("linux"), nil, "agent", "install", "--project", h.project)
	if err != nil {
		t.Fatalf("a linger failure must not fail the install: %v", err)
	}
	if !strings.Contains(out, "run: loginctl enable-linger guy") {
		t.Fatalf("output does not name the command:\n%s", out)
	}
}

func TestAgentUninstall(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h := newFakeHost(t)
			if _, _, err := run(t, h.app(goos), nil, "agent", "install", "--project", h.project); err != nil {
				t.Fatalf("install: %v", err)
			}
			h.calls = nil
			out, _, err := run(t, h.app(goos), nil, "agent", "uninstall", "--project", h.project)
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			id := h.id(t)
			var unit string
			var want []string
			if goos == "darwin" {
				label := "ai.aeryx.sock-runner." + id
				unit = filepath.Join(h.home, "Library", "LaunchAgents", label+".plist")
				want = []string{fmt.Sprintf("launchctl bootout gui/%d/%s", os.Getuid(), label)}
			} else {
				label := "sock-runner-" + id + ".service"
				unit = filepath.Join(h.home, ".config", "systemd", "user", label)
				want = []string{"systemctl --user disable --now " + label, "systemctl --user daemon-reload"}
			}
			if strings.Join(h.calls, "\n") != strings.Join(want, "\n") {
				t.Fatalf("calls:\n%s", strings.Join(h.calls, "\n"))
			}
			if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unit left: %v", err)
			}
			if !strings.Contains(out, "removed") {
				t.Fatalf("output %q", out)
			}
			// the skill serves every project: uninstalling one leaves it
			if _, err := os.Stat(h.skillPath()); err != nil {
				t.Fatalf("skill removed: %v", err)
			}
		})
	}
}

func TestAgentStatus(t *testing.T) {
	h := newFakeHost(t)
	out, _, err := run(t, h.app("darwin"), nil, "agent", "status", "--project", h.project)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "not installed") {
		t.Fatalf("output %q", out)
	}
	if _, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project); err != nil {
		t.Fatal(err)
	}
	label := "ai.aeryx.sock-runner." + h.id(t)
	h.calls = nil
	out, _, err = run(t, h.app("darwin"), nil, "agent", "status", "--project", h.project)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if want := fmt.Sprintf("launchctl print gui/%d/%s", os.Getuid(), label); len(h.calls) != 1 || h.calls[0] != want {
		t.Fatalf("calls %v", h.calls)
	}
	for _, want := range []string{label, "loaded", "listen.log"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	h.fail[h.calls[0]] = errors.New("exit status 113")
	out, _, _ = run(t, h.app("darwin"), nil, "agent", "status", "--project", h.project)
	if !strings.Contains(out, "not loaded") {
		t.Fatalf("output %q", out)
	}
}

func TestAgentInstallReplacesSkillSockWrote(t *testing.T) {
	h := newFakeHost(t)
	writeFile(t, h.skillPath(), "---\nname: sock\n---\n"+agentSkillMarker+"\nold\n")
	if _, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, _ := os.ReadFile(h.skillPath()); string(got) != agentSkillMD {
		t.Fatal("skill sock agent install wrote not replaced")
	}
}

// Only a file holding agent install's own marker is replaced: not another
// skill, and not the one sock skill install writes to the same path.
func TestAgentInstallKeepsOtherSkills(t *testing.T) {
	for name, old := range map[string]string{"foreign": "mine\n", "sock skill install": skillMD} {
		t.Run(name, func(t *testing.T) {
			h := newFakeHost(t)
			writeFile(t, h.skillPath(), old)
			out, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if got, _ := os.ReadFile(h.skillPath()); string(got) != old {
				t.Fatal("overwrote a skill sock agent install did not write")
			}
			if !strings.Contains(out, "not written by sock agent install") {
				t.Fatalf("output does not say why:\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(h.home, "Library", "LaunchAgents", "ai.aeryx.sock-runner."+h.id(t)+".plist")); err != nil {
				t.Fatalf("unit not installed: %v", err)
			}
		})
	}
}

// A symlinked skill directory could point the write anywhere: refuse it,
// still install the unit.
func TestAgentInstallRefusesSymlinkedSkillDir(t *testing.T) {
	for _, link := range []string{filepath.Join(".agents", "skills"), filepath.Join(".agents", "skills", "sock"), filepath.Join(".agents", "skills", "sock", "SKILL.md")} {
		t.Run(link, func(t *testing.T) {
			h := newFakeHost(t)
			target := t.TempDir()
			path := filepath.Join(h.home, link)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			out, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project)
			if err != nil {
				t.Fatalf("install: %v", err)
			}
			if entries, _ := os.ReadDir(target); len(entries) != 0 {
				t.Fatalf("wrote through the symlink: %v", entries)
			}
			if !strings.Contains(out, "symlink") {
				t.Fatalf("output does not say why:\n%s", out)
			}
		})
	}
}

// The unit runs in the project, and jq runs before the owner gate: a PATH
// entry that resolves against the working directory would run the project's
// files.
func TestAgentInstallRefusesRelativePathEntries(t *testing.T) {
	for i, entry := range []string{"", ".", "bin"} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			h := newFakeHost(t)
			h.env["PATH"] = h.bin + ":" + entry + ":/usr/bin"
			_, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", h.project)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("PATH entry %q", entry)) {
				t.Fatalf("err %v, want the entry named", err)
			}
			if len(h.calls) != 0 {
				t.Fatalf("ran %v", h.calls)
			}
		})
	}
}

// XDG base directory spec: a relative path is invalid and is ignored.
func TestAgentInstallIgnoresRelativeXDG(t *testing.T) {
	t.Chdir(t.TempDir()) // a regression writes there, not into the repo
	h := newFakeHost(t)
	h.env["XDG_DATA_HOME"] = "data"
	h.env["XDG_STATE_HOME"] = "state"
	h.env["XDG_CONFIG_HOME"] = "config"
	if _, _, err := run(t, h.app("linux"), nil, "agent", "install", "--project", h.project); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, p := range []string{
		filepath.Join(h.home, ".local", "share", "sock", "runner", "1.2.3", "sock-runner"),
		filepath.Join(h.home, ".local", "state", "sock-integrations", "runner", h.id(t)),
		filepath.Join(h.home, ".config", "systemd", "user", "sock-runner-"+h.id(t)+".service"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

func TestAgentUninstallAfterProjectDeleted(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h := newFakeHost(t)
			h.project = filepath.Join(h.project, "gone")
			if err := os.Mkdir(h.project, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, _, err := run(t, h.app(goos), nil, "agent", "install", "--project", h.project); err != nil {
				t.Fatalf("install: %v", err)
			}
			dir := filepath.Join(h.home, "Library", "LaunchAgents")
			if goos == "linux" {
				dir = filepath.Join(h.home, ".config", "systemd", "user")
			}
			if err := os.Remove(h.project); err != nil {
				t.Fatal(err)
			}
			out, _, err := run(t, h.app(goos), nil, "agent", "uninstall", "--project", h.project)
			if err != nil {
				t.Fatalf("uninstall: %v", err)
			}
			if units, _ := os.ReadDir(dir); len(units) != 0 {
				t.Fatalf("unit left: %v", units)
			}
			if !strings.Contains(out, "removed") {
				t.Fatalf("output %q", out)
			}
		})
	}
}

func TestAgentInstallRefusesUnsafePath(t *testing.T) {
	h := newFakeHost(t)
	odd := filepath.Join(h.project, "a b")
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, h.app("darwin"), nil, "agent", "install", "--project", odd); err == nil {
		t.Fatal("installed a unit for a path a unit file would need quoted")
	}
	if len(h.calls) != 0 {
		t.Fatalf("ran %v", h.calls)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
