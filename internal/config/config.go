// Package config resolves the server URL, token and access gate: flag, then
// env, then file, then DefaultURL for the URL. It also writes the token that
// sock login and sock register obtain.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
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
	// register, which run before a token exists.
	Anonymous bool
}

// DefaultPath is $XDG_CONFIG_HOME/sock/config.toml, else ~/.config/sock/config.toml.
func DefaultPath(getenv func(string) string) (string, error) {
	return client.DefaultConfigPath(getenv)
}

// Load resolves and validates the config.
func Load(s Sources) (Config, error) {
	c := Config{Config: client.Config{URL: DefaultURL}}
	f, err := client.LoadFileConfig(s.Path)
	if err != nil {
		return Config{}, err
	}
	if f.URL != "" {
		c.URL = f.URL
	}
	c.Token = f.Token
	c.AccessGate = f.AccessGate
	if v := s.Getenv("SOCK_URL"); v != "" {
		c.URL = v
	}
	if v := s.Getenv("SOCK_TOKEN"); v != "" {
		c.Token = v
	}
	if v := s.Getenv("SOCK_ACCESS_GATE"); v != "" {
		c.AccessGate = v
	}
	if s.FlagURL != "" {
		c.URL = s.FlagURL
	}
	if s.FlagToken != "" {
		c.Token = s.FlagToken
	}
	// A trailing newline from a secret file or `$(cat ...)` is not part of the value.
	c.Token = strings.TrimSpace(c.Token)
	c.AccessGate = strings.TrimSpace(c.AccessGate)
	if err := validate(c, s.Anonymous); err != nil {
		return Config{}, err
	}
	return c, nil
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
func SetNewToken(path, token string) error {
	return rewrite(path, func(m map[string]any) error {
		if s, _ := m["token"].(string); strings.TrimSpace(s) != "" {
			return ErrTokenExists
		}
		m["token"] = token
		return nil
	})
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
		return fmt.Errorf("config: parse %s: %w", path, err)
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
