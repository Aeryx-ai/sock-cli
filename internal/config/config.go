// Package config resolves the server URL, token and access gate: flag, then
// env, then file, then DefaultURL for the URL. It also writes the token that
// sock login and sock register obtain.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	client "github.com/guygrigsby/sock-client"
)

// DefaultURL is the public sock hub, used when no flag, env or file names one.
const DefaultURL = client.DefaultURL

// Config is what every command needs to reach the server: client.Config,
// whose AccessGate is the staging gate resolved from flag, env and file.
type Config struct {
	client.Config
}

// Sources are the inputs to Load, injectable for tests.
type Sources struct {
	FlagURL   string
	FlagToken string
	Getenv    func(string) string
	// Path is the config file. A missing file is not an error.
	Path string
	// Anonymous skips the token requirement, for sock login and sock
	// register, which run before a token exists. A token from the file is
	// left out when another source names the server.
	Anonymous bool
	// FileTokenOnly ignores SOCK_TOKEN and --token, for sock logout, which
	// signs out the file's session.
	FileTokenOnly bool
}

// DefaultPath is $XDG_CONFIG_HOME/sock/config.toml, else ~/.config/sock/config.toml.
func DefaultPath(getenv func(string) string) (string, error) {
	return client.DefaultConfigPath(getenv)
}

// Load resolves and validates the config. A token from the file goes only to
// the file's server: the URL and gate then come from the file too, and a
// SOCK_URL, --url or SOCK_ACCESS_GATE naming another is refused. One source,
// never a mix, so a repo's .envrc cannot redirect the session.
func Load(s Sources) (Config, error) {
	var c Config
	f, err := client.LoadFileConfig(s.Path)
	if err != nil {
		return Config{}, err
	}
	fileURL, fileGate := f.URL, strings.TrimSpace(f.AccessGate)
	if fileURL == "" {
		fileURL = DefaultURL
	}
	c.URL = fileURL
	if v := s.Getenv("SOCK_URL"); v != "" {
		c.URL = v
	}
	if s.FlagURL != "" {
		c.URL = s.FlagURL
	}
	c.Token = s.Getenv("SOCK_TOKEN")
	if s.FlagToken != "" {
		c.Token = s.FlagToken
	}
	// The file's gate goes only to the file's server.
	c.AccessGate = s.Getenv("SOCK_ACCESS_GATE")
	if c.AccessGate == "" && SameServer(c.URL, fileURL) {
		c.AccessGate = fileGate
	}
	// A trailing newline from a secret file or `$(cat ...)` is not part of the value.
	c.Token = strings.TrimSpace(c.Token)
	c.AccessGate = strings.TrimSpace(c.AccessGate)
	if s.FileTokenOnly || (s.FlagToken == "" && s.Getenv("SOCK_TOKEN") == "") {
		c.Token = ""
		if fileToken := strings.TrimSpace(f.Token); fileToken != "" {
			switch why := mixedSources(s, fileURL, fileGate); {
			case why == "":
				c.Token, c.URL, c.AccessGate = fileToken, fileURL, fileGate
			case !s.Anonymous:
				return Config{}, fmt.Errorf("%s holds the token, so the server comes from it too, but %s; unset it, or set url and access_gate in the file", s.Path, why)
			}
			// anonymous and another server named: the token is left out
		}
	}
	if err := validate(c, s.Anonymous); err != nil {
		return Config{}, err
	}
	return c, nil
}

// mixedSources names the flag or variable that points a file token at a
// server other than fileURL and fileGate, or "" when none does.
func mixedSources(s Sources, fileURL, fileGate string) string {
	switch {
	case s.FlagURL != "" && !SameServer(s.FlagURL, fileURL):
		return "--url names another server"
	case s.Getenv("SOCK_URL") != "" && !SameServer(s.Getenv("SOCK_URL"), fileURL):
		return "SOCK_URL names another server"
	case s.Getenv("SOCK_ACCESS_GATE") != "" && strings.TrimSpace(s.Getenv("SOCK_ACCESS_GATE")) != fileGate:
		return "SOCK_ACCESS_GATE names another gate"
	}
	return ""
}

// anonymousToken stands in for the token when an anonymous load validates
// the URL with the shared rules, which check a token too.
const anonymousToken = "sock_st_anonymous"

func validate(c Config, anonymous bool) error {
	// The CLI names its own sources in these two hints.
	if c.URL == "" {
		return errors.New("no server URL: set SOCK_URL, --url, or url in the config file")
	}
	if strings.ContainsFunc(c.AccessGate, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("access gate contains whitespace or control characters; check SOCK_ACCESS_GATE or access_gate in the config file")
	}
	if anonymous && c.Token == "" {
		cc := c.Config
		cc.Token = anonymousToken
		return client.Validate(cc)
	}
	if c.Token == "" {
		return errors.New("no token: run sock login, or set SOCK_TOKEN, --token, or token in the config file")
	}
	return client.Validate(c.Config)
}

// FileToken returns the token the config file holds, or "" when it holds
// none or does not exist.
func FileToken(path string) (string, error) {
	f, err := client.LoadFileConfig(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(f.Token), nil
}

// ErrTokenExists means the config file already holds a token.
var ErrTokenExists = errors.New("config file already holds a token")

// SetNewToken writes token into the config file, keeping every other key,
// and refuses with ErrTokenExists when the file already holds a token. The
// check runs on the file as read for the rewrite, so a token written since
// an earlier check is never lost. The directory is made 0700 and the file
// 0600, written to a temporary file and renamed so a crash never leaves a
// half-written file.
//
// Load sends a file token only to the file's server, so url and gate, the
// server the token was minted at, are written beside it where the file has
// none (url only when it is not DefaultURL); a file naming another is refused.
func SetNewToken(path, token, url, gate string) error {
	return rewrite(path, func(m map[string]any) error {
		if s, _ := m["token"].(string); strings.TrimSpace(s) != "" {
			return ErrTokenExists
		}
		fileURL, _ := m["url"].(string)
		fileGate, _ := m["access_gate"].(string)
		if why := serverConflict(fileURL, fileGate, url, gate); why != "" {
			return errors.New(why)
		}
		m["token"] = token
		if url == DefaultURL {
			url = ""
		}
		fillServer(m, url, gate)
		return nil
	})
}

// SetPuppetToken writes the puppet token sock-mcp reads into the config file
// under puppet_token, replacing any earlier one and keeping every other key,
// the human's token included. Written like SetNewToken.
//
// sock-mcp reads the URL and access gate from the same file, so a key the file
// lacks is filled with the url and gate the token was minted against. A key it
// has is left alone: PuppetFileConflict refuses before a token is minted for a
// different one.
func SetPuppetToken(path, token, url, gate string) error {
	return rewrite(path, func(m map[string]any) error {
		m["puppet_token"] = token
		fillServer(m, url, gate)
		return nil
	})
}

// ErrMachineKeyExists means the config file already holds another machine key.
var ErrMachineKeyExists = errors.New("config file already holds another machine_key")

// SetMachineKey writes the machine key sock-mcp reads into the config file
// under machine_key, keeping every other key and filling url and gate like
// SetPuppetToken. Another machine key already there is ErrMachineKeyExists
// unless force; the same one is left as it is. A file naming another url or
// gate is refused, as PuppetFileConflict says.
func SetMachineKey(path, key, url, gate string, force bool) error {
	return rewrite(path, func(m map[string]any) error {
		if s, _ := m["machine_key"].(string); !force && strings.TrimSpace(s) != "" && strings.TrimSpace(s) != key {
			return ErrMachineKeyExists
		}
		// on the file as rewritten: a url written since any earlier check
		// is never paired with this key
		fileURL, _ := m["url"].(string)
		fileGate, _ := m["access_gate"].(string)
		if why := serverConflict(fileURL, fileGate, url, gate); why != "" {
			return errors.New(why)
		}
		m["machine_key"] = key
		fillServer(m, url, gate)
		return nil
	})
}

// fillServer sets url and access_gate where the file has none: sock-mcp sends
// a credential from the file to the file's url.
func fillServer(m map[string]any, url, gate string) {
	if _, ok := m["url"]; !ok && url != "" {
		m["url"] = url
	}
	if _, ok := m["access_gate"]; !ok && gate != "" {
		m["access_gate"] = gate
	}
}

// PuppetFileConflict says why a puppet token minted against url and gate
// cannot be saved in the file at path: the file already names another url or
// gate, and sock-mcp would send the token there. "" means it can be saved.
func PuppetFileConflict(path, url, gate string) (string, error) {
	f, err := client.LoadFileConfig(path)
	if err != nil {
		return "", err
	}
	return serverConflict(f.URL, f.AccessGate, url, gate), nil
}

// serverConflict says why a credential for url and gate cannot sit in a file
// naming fileURL and fileGate, or "" when it can.
func serverConflict(fileURL, fileGate, url, gate string) string {
	if fileURL != "" && !SameServer(fileURL, url) {
		return "the config file's url differs from the server this command is using; unset SOCK_URL and --url, or change url in the file"
	}
	if fileGate != "" && fileGate != gate {
		return "the config file's access_gate differs from the one this command is using; unset SOCK_ACCESS_GATE or change access_gate in the file"
	}
	return ""
}

// RemoveToken deletes the token from the config file, keeping every other
// key. A missing file is not an error.
func RemoveToken(path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return rewrite(path, func(m map[string]any) error {
		delete(m, "token")
		return nil
	})
}

func rewrite(path string, edit func(map[string]any) error) error {
	if path == "" {
		return errors.New("config: no config path: set XDG_CONFIG_HOME or HOME")
	}
	m := map[string]any{}
	if _, err := toml.DecodeFile(path, &m); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return client.ParseError(path, err)
	}
	if err := edit(m); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// sock's own directory: tighten it if it was created looser.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml") // created 0600
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: sync %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: replace %s: %w", path, err)
	}
	return nil
}

// SameServer reports whether two server URLs name one server: scheme and host
// without case, the path without a trailing slash.
func SameServer(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host) &&
		strings.TrimRight(ua.Path, "/") == strings.TrimRight(ub.Path, "/")
}
