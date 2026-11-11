package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// The runner and skill sock agent install writes. Copies: sock-integrations
// is canonical (runner/ and agents/skills/sock/), agent/SOURCE records the
// commit they came from, `make agent-assets` refreshes them and
// TestAgentAssetsMatchIntegrations catches drift.
var (
	//go:embed agent/sock-runner
	runnerScript string
	//go:embed agent/lib.sh
	runnerLib string
	//go:embed agent/SKILL.md
	agentSkillMD string
	//go:embed agent/SOURCE
	agentSource string

	runnerFiles = map[string]string{"sock-runner": runnerScript, "lib.sh": runnerLib}
)

// agentSkillMarker is in every skill sock agent install writes: a file at the
// skill's path holding it is sock's to replace.
const agentSkillMarker = "<!-- written by sock; sock agent install replaces this file -->"

type serviceKind int

const (
	launchd serviceKind = iota
	systemd
)

// runnerUnit is one project's user service, as sock-runner-service in
// sock-integrations writes it: same label, same file, so an install by either
// replaces the other's.
type runnerUnit struct {
	Label, Sock, Project, Runner, Log string
	Env                               []string // name=value, in unit order
}

func (u runnerUnit) render(kind serviceKind) string {
	var b strings.Builder
	if kind == systemd {
		fmt.Fprintf(&b, "[Unit]\nDescription=sock-runner for %s\nAfter=network-online.target\n\n[Service]\n", u.Project)
		fmt.Fprintf(&b, "ExecStart=%s listen --as-puppet --project %s --exec %s\nWorkingDirectory=%s\n", u.Sock, u.Project, u.Runner, u.Project)
		for _, e := range u.Env {
			fmt.Fprintf(&b, "Environment=%s\n", e)
		}
		fmt.Fprintf(&b, "Restart=always\nRestartSec=5\nStandardError=append:%s\n\n[Install]\nWantedBy=default.target\n", u.Log)
		return b.String()
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
`)
	fmt.Fprintf(&b, "\t<string>%s</string>\n\t<key>ProgramArguments</key>\n\t<array>\n", u.Label)
	for _, a := range []string{u.Sock, "listen", "--as-puppet", "--project", u.Project, "--exec", u.Runner} {
		fmt.Fprintf(&b, "\t\t<string>%s</string>\n", a)
	}
	fmt.Fprintf(&b, "\t</array>\n\t<key>WorkingDirectory</key>\n\t<string>%s</string>\n\t<key>EnvironmentVariables</key>\n\t<dict>\n", u.Project)
	for _, e := range u.Env {
		k, v, _ := strings.Cut(e, "=")
		fmt.Fprintf(&b, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", k, v)
	}
	fmt.Fprintf(&b, "\t</dict>\n\t<key>RunAtLoad</key>\n\t<true/>\n\t<key>KeepAlive</key>\n\t<true/>\n\t<key>ThrottleInterval</key>\n\t<integer>10</integer>\n\t<key>StandardErrorPath</key>\n\t<string>%s</string>\n</dict>\n</plist>\n", u.Log)
	return b.String()
}

// Values go into a unit file unquoted and through sh -c in sock listen, as in
// sock-runner-service: refuse what either would have to quote.
var (
	safePath    = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)
	safeSetting = regexp.MustCompile(`^[A-Za-z_]+=[A-Za-z0-9._/@+:-]+$`)
)

// agentHost is where one project's service lives on this machine.
type agentHost struct {
	kind                         serviceKind
	home, project, id            string
	label, unit, domain, service string // domain and service: launchd's gui/<uid> and gui/<uid>/<label>
	state                        string
}

func (a *App) agentHost(projectFlag string) (agentHost, error) {
	h := agentHost{home: a.Getenv("HOME")}
	if h.home == "" {
		return h, errors.New("HOME is not set")
	}
	switch a.OS {
	case "darwin":
		h.kind = launchd
	case "linux":
		h.kind = systemd
	default:
		return h, fmt.Errorf("no service manager for %s: run sock listen --as-puppet --exec <sock-runner> yourself", a.OS)
	}
	dir := projectFlag
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err == nil {
		h.project, err = filepath.EvalSymlinks(abs)
	}
	if err != nil {
		return h, fmt.Errorf("project: %w", err)
	}
	if fi, err := os.Stat(h.project); err != nil || !fi.IsDir() {
		return h, fmt.Errorf("%s is not a directory", h.project)
	}
	if !safePath.MatchString(h.project) {
		return h, fmt.Errorf("the project path may hold only A-Z a-z 0-9 . _ / @ + -: %s", h.project)
	}
	sum := sha256.Sum256([]byte(h.project))
	h.id = hex.EncodeToString(sum[:])[:16]
	if h.kind == launchd {
		h.label = "ai.aeryx.sock-runner." + h.id
		h.unit = filepath.Join(h.home, "Library", "LaunchAgents", h.label+".plist")
		h.domain = fmt.Sprintf("gui/%d", os.Getuid())
		h.service = h.domain + "/" + h.label
	} else {
		h.label = "sock-runner-" + h.id + ".service"
		h.unit = filepath.Join(a.xdg("XDG_CONFIG_HOME", h.home, ".config"), "systemd", "user", h.label)
	}
	h.state = filepath.Join(a.xdg("XDG_STATE_HOME", h.home, ".local/state"), "sock-integrations", "runner", h.id)
	return h, nil
}

// xdg is the XDG base directory env names, else home/fallback.
func (a *App) xdg(env, home, fallback string) string {
	if d := a.Getenv(env); d != "" {
		return d
	}
	return filepath.Join(home, fallback)
}

// checkCmd prints the command that shows whether the unit runs.
func (h agentHost) checkCmd() string {
	if h.kind == launchd {
		return "launchctl print " + h.service
	}
	return "systemctl --user status " + h.label
}

func (h agentHost) log() string { return filepath.Join(h.state, "listen.log") }

// runCmd runs a service manager command; its output becomes the error text.
func (a *App) runCmd(ctx context.Context, name string, args ...string) error {
	out, err := a.RunCmd(ctx, nil, name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return nil
}

func runCommand(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	return cmd.CombinedOutput()
}

func newAgentCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Keep an agent answering your sock messages for a project, as a user service",
	}
	var project string
	cmd.PersistentFlags().StringVar(&project, "project", "", "the project directory (default: the working directory)")
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install and start the project's runner service and the sock skill; rerun to update",
		Long: "Installs a launchd agent (macOS) or systemd --user unit (Linux) that runs sock listen --as-puppet --project <dir> --exec sock-runner, " +
			"enables linger on Linux and writes the sock skill to ~/.agents/skills/sock/SKILL.md. " +
			"Runner settings come from SOCK_RUNNER_AGENT (default claude), SOCK_RUNNER_PERMISSION (default default) and, when set, " +
			"SOCK_RUNNER_SESSION, SOCK_RUNNER_MODEL, SOCK_RUNNER_TIMEOUT and SOCK_RUNNER_ALLOW_BYPASS. The unit holds no credential.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := app.agentHost(project)
			if err != nil {
				return err
			}
			return app.agentInstall(cmd.Context(), h, cmd.OutOrStdout())
		},
	}, &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the project's runner service",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := app.agentHost(project)
			if err != nil {
				return err
			}
			if h.kind == launchd {
				_, _ = app.RunCmd(cmd.Context(), nil, "launchctl", "bootout", h.service) // none loaded is fine
			} else {
				_, _ = app.RunCmd(cmd.Context(), nil, "systemctl", "--user", "disable", "--now", h.label)
			}
			if err := os.Remove(h.unit); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", h.unit, err)
			}
			if h.kind == systemd {
				if err := app.runCmd(cmd.Context(), "systemctl", "--user", "daemon-reload"); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", h.label)
			return err
		},
	}, &cobra.Command{
		Use:   "status",
		Short: "Say whether the project's runner service is installed and loaded, and where it logs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h, err := app.agentHost(project)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if _, err := os.Stat(h.unit); err != nil {
				_, err = fmt.Fprintf(w, "%s not installed: run sock agent install\n", h.label)
				return err
			}
			loaded := "loaded"
			name, args, _ := strings.Cut(h.checkCmd(), " ")
			if h.kind == systemd {
				name, args = "systemctl", "--user is-active --quiet "+h.label
			}
			if _, err := app.RunCmd(cmd.Context(), nil, name, strings.Fields(args)...); err != nil {
				loaded = "not loaded"
			}
			_, err = fmt.Fprintf(w, "%s %s\nunit: %s\nlog:  %s\n", h.label, loaded, h.unit, h.log())
			return err
		},
	})
	return cmd
}

func (a *App) agentInstall(ctx context.Context, h agentHost, w io.Writer) error {
	sockBin, err := exec.LookPath("sock")
	if err == nil {
		sockBin, err = filepath.Abs(sockBin)
	}
	if err != nil {
		return fmt.Errorf("sock is not on PATH, and the service runs it from there: %w", err)
	}
	data := filepath.Join(a.xdg("XDG_DATA_HOME", h.home, ".local/share"), "sock", "runner", a.Version)
	env := []string{"PATH=" + a.Getenv("PATH"), "SOCK_RUNNER_AGENT=" + orDefault(a.Getenv("SOCK_RUNNER_AGENT"), "claude"),
		"SOCK_RUNNER_PERMISSION=" + orDefault(a.Getenv("SOCK_RUNNER_PERMISSION"), "default"),
		"SOCK_RUNNER_WORKDIR=" + h.project, "SOCK_RUNNER_STATE=" + h.state}
	for _, n := range []string{"SOCK_RUNNER_SESSION", "SOCK_RUNNER_MODEL", "SOCK_RUNNER_TIMEOUT", "SOCK_RUNNER_ALLOW_BYPASS"} {
		if v := a.Getenv(n); v != "" {
			env = append(env, n+"="+v)
		}
	}
	for _, p := range []string{sockBin, data} {
		if !safePath.MatchString(p) {
			return fmt.Errorf("the path may hold only A-Z a-z 0-9 . _ / @ + -: %s", p)
		}
	}
	for _, e := range env {
		if !safeSetting.MatchString(e) {
			k, _, _ := strings.Cut(e, "=")
			return fmt.Errorf("a setting holds a character a unit file would need quoted: %s", k)
		}
	}

	for name, content := range runnerFiles {
		mode := os.FileMode(0o644)
		if name == "sock-runner" {
			mode = 0o755
		}
		if err := writeFileAtomic(filepath.Join(data, name), content, mode); err != nil {
			return fmt.Errorf("install the runner: %w", err)
		}
	}
	// the runner's own check, so a unit never starts with settings it refuses at every message
	if out, err := a.RunCmd(ctx, env, "/bin/sh", "-c", `cmd_name=runner; . "$1" && runner_config`, "sh", filepath.Join(data, "lib.sh")); err != nil {
		return fmt.Errorf("%s (%w)", bytes.TrimSpace(out), err)
	}

	if err := os.MkdirAll(h.state, 0o700); err != nil {
		return err
	}
	u := runnerUnit{Label: h.label, Sock: sockBin, Project: h.project, Runner: filepath.Join(data, "sock-runner"), Log: h.log(), Env: env}
	if err := writeFileAtomic(h.unit, u.render(h.kind), 0o600); err != nil {
		return err
	}
	if h.kind == launchd {
		// bootout first so a reinstall picks up the new file; none loaded is fine
		_, _ = a.RunCmd(ctx, nil, "launchctl", "bootout", h.service)
		if err := a.runCmd(ctx, "launchctl", "bootstrap", h.domain, h.unit); err != nil {
			return err
		}
	} else {
		for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", h.label}, {"restart", h.label}} {
			// enable --now leaves a running unit on its old file: restart picks up this one
			if err := a.runCmd(ctx, "systemctl", append([]string{"--user"}, args...)...); err != nil {
				return err
			}
		}
	}
	_, _ = fmt.Fprintf(w, "installed %s\nunit:   %s\nrunner: %s\nlog:    %s (runner.log beside it)\ncheck:  %s\n", h.label, h.unit, u.Runner, h.log(), h.checkCmd())

	if h.kind == systemd {
		// without linger the unit stops at logout and does not start at boot
		name := a.Getenv("USER")
		if u, err := user.Current(); name == "" && err == nil {
			name = u.Username
		}
		if err := a.runCmd(ctx, "loginctl", "enable-linger", name); err != nil {
			_, _ = fmt.Fprintf(w, "linger: could not enable it, so the service stops when you log out; run: loginctl enable-linger %s\n", name)
		} else {
			_, _ = fmt.Fprintf(w, "linger: enabled for %s, so the service runs without a login\n", name)
		}
	}

	dir := filepath.Join(h.home, ".agents", "skills", skillName)
	path := filepath.Join(dir, "SKILL.md")
	if old, err := os.ReadFile(path); err == nil && string(old) != skillMD && !strings.Contains(string(old), agentSkillMarker) {
		_, err = fmt.Fprintf(w, "skill:  left %s alone: not written by sock\n", path)
		return err
	}
	if _, err := installSkill(dir, agentSkillMD); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "skill:  %s\n", path)
	return err
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// writeFileAtomic writes content to path through a temporary file and a
// rename, so a reader never sees half a file.
func writeFileAtomic(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }() // no-op after the rename
	_, werr := f.WriteString(content)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(f.Name(), mode)
	}
	if werr == nil {
		werr = os.Rename(f.Name(), path)
	}
	return werr
}
