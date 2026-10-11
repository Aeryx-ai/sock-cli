// Package config resolves the server URL and token: flag, then env, then file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is what every command needs to reach the server.
type Config struct {
	URL   string
	Token string
}

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
	var c Config
	f, err := readFile(s.Path)
	if err != nil {
		return Config{}, err
	}
	c.URL, c.Token = f.URL, f.Token
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
	if c.URL == "" {
		return errors.New("no server URL: set SOCK_URL, --url, or url in the config file")
	}
	if c.Token == "" {
		return errors.New("no token: set SOCK_TOKEN, --token, or token in the config file")
	}
	if !strings.HasPrefix(c.Token, "sock_pt_") && !strings.HasPrefix(c.Token, "sock_st_") {
		return errors.New("token must start with sock_pt_ or sock_st_")
	}
	// Error text never echoes the raw URL: it may carry credentials.
	u, err := url.Parse(c.URL)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // url.Error repeats the input; keep only the reason
		}
		return fmt.Errorf("server URL is not a valid URL: %w", err)
	}
	if u.User != nil {
		return errors.New("server URL must not contain a user or password; put the token in SOCK_TOKEN")
	}
	// A scheme-less "user:pass@host" parses as scheme "user" with the rest
	// in Opaque, so refuse Opaque and never print the URL here.
	if u.Host == "" || u.Opaque != "" {
		return errors.New("server URL is not a valid absolute https URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
	}
	// Only scheme and host: the path or query may carry secrets too.
	return fmt.Errorf("server URL %s://%s must use https (http only for loopback)", u.Scheme, u.Host)
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
