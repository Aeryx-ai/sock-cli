// Package config resolves the server URL and token: flag, then env, then
// file, then DefaultURL for the URL.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	client "github.com/guygrigsby/sock-client"
)

// DefaultURL is the public sock hub, used when no flag, env or file names one.
const DefaultURL = client.DefaultURL

// Config is what every command needs to reach the server.
type Config = client.Config

// Sources are the inputs to Load, injectable for tests.
type Sources struct {
	FlagURL   string
	FlagToken string
	Getenv    func(string) string
	// Path is the config file. A missing file is not an error.
	Path string
}

type file struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
}

// DefaultPath is $XDG_CONFIG_HOME/sock/config.toml, else ~/.config/sock/config.toml.
func DefaultPath(getenv func(string) string) (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "sock", "config.toml"), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errors.New("config: neither XDG_CONFIG_HOME nor HOME is set")
	}
	return filepath.Join(home, ".config", "sock", "config.toml"), nil
}

// Load resolves and validates the config.
func Load(s Sources) (Config, error) {
	c := Config{URL: DefaultURL}
	f, err := readFile(s.Path)
	if err != nil {
		return Config{}, err
	}
	if f.URL != "" {
		c.URL = f.URL
	}
	c.Token = f.Token
	if v := s.Getenv("SOCK_URL"); v != "" {
		c.URL = v
	}
	if v := s.Getenv("SOCK_TOKEN"); v != "" {
		c.Token = v
	}
	if s.FlagURL != "" {
		c.URL = s.FlagURL
	}
	if s.FlagToken != "" {
		c.Token = s.FlagToken
	}
	// A trailing newline from a secret file or `$(cat ...)` is not part of the token.
	c.Token = strings.TrimSpace(c.Token)
	if err := validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func readFile(path string) (file, error) {
	var f file
	if path == "" {
		return f, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("config: %w", err)
	}
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return f, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if f.Token != "" && info.Mode().Perm()&0o077 != 0 {
		return file{}, fmt.Errorf("config: %s holds a token but is readable by others (mode %o); run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return f, nil
}

func validate(c Config) error {
	// The CLI names its own sources in these two hints.
	if c.URL == "" {
		return errors.New("no server URL: set SOCK_URL, --url, or url in the config file")
	}
	if c.Token == "" {
		return errors.New("no token: set SOCK_TOKEN, --token, or token in the config file")
	}
	return client.Validate(c)
}
