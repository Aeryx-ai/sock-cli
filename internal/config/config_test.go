package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const tok = "sock_pt_0b3f_secret"

func TestPrecedenceFlagOverEnvOverFile(t *testing.T) {
	p := writeFile(t, 0o600, "url = \"https://file.example\"\ntoken = \"sock_pt_file\"\n")
	cases := []struct {
		name      string
		src       Sources
		wantURL   string
		wantToken string
	}{
		{"file only", Sources{Getenv: env(nil), Path: p}, "https://file.example", "sock_pt_file"},
		{"env beats file", Sources{Getenv: env(map[string]string{"SOCK_URL": "https://env.example", "SOCK_TOKEN": "sock_pt_env"}), Path: p}, "https://env.example", "sock_pt_env"},
		{"flag beats env", Sources{FlagURL: "https://flag.example", FlagToken: "sock_st_flag", Getenv: env(map[string]string{"SOCK_URL": "https://env.example", "SOCK_TOKEN": "sock_pt_env"}), Path: p}, "https://flag.example", "sock_st_flag"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Load(c.src)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got.URL != c.wantURL || got.Token != c.wantToken {
				t.Fatalf("got %+v, want url %q token %q", got, c.wantURL, c.wantToken)
			}
		})
	}
}

func TestMissingFileIsFine(t *testing.T) {
	got, err := Load(Sources{Getenv: env(map[string]string{"SOCK_URL": "https://x.example", "SOCK_TOKEN": tok}), Path: filepath.Join(t.TempDir(), "nope.toml")})
	if err != nil || got.Token != tok {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestReadableFileWithTokenRefused(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		p := writeFile(t, mode, "url = \"https://x.example\"\ntoken = \""+tok+"\"\n")
		_, err := Load(Sources{Getenv: env(nil), Path: p})
		if err == nil || !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), p) {
			t.Fatalf("mode %o: want refusal naming file and chmod 600, got %v", mode, err)
		}
	}
}

func TestReadableFileWithoutTokenAllowed(t *testing.T) {
	p := writeFile(t, 0o644, "url = \"https://x.example\"\n")
	got, err := Load(Sources{Getenv: env(map[string]string{"SOCK_TOKEN": tok}), Path: p})
	if err != nil || got.URL != "https://x.example" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name, url, token, wantErr string
	}{
		{"no url", "", tok, "no server URL"},
		{"no token", "https://x.example", "", "no token"},
		{"whitespace only token", "https://x.example", " \t\n", "no token"},
		{"internal whitespace token", "https://x.example", "sock_pt_abc def", "whitespace"},
		{"bad prefix", "https://x.example", "ghp_abc", "token must start with sock_pt_ or sock_st_"},
		{"plain http remote", "http://x.example", tok, "https"},
		{"http loopback ok", "http://127.0.0.1:9", tok, ""},
		{"http localhost ok", "http://localhost:9", tok, ""},
		{"http LOCALHOST ok", "http://LocalHost:9", tok, ""},
		{"garbage url", "::::", tok, "URL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load(Sources{FlagURL: c.url, FlagToken: c.token, Getenv: env(nil), Path: filepath.Join(t.TempDir(), "none")})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestURLCredentialsRefusedWithoutEcho(t *testing.T) {
	for name, u := range map[string]string{
		"userinfo":          "https://alice:hunter2@x.example",
		"user only":         "https://hunter2@x.example",
		"unparseable":       "https://alice:hunter2@ho st",
		"no host":           "https://alice:hunter2@",
		"scheme-less":       "alice:hunter2@x.example",
		"plain http remote": "http://alice:hunter2@x.example",
		"secret in query":   "http://x.example/p?token=hunter2",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(Sources{FlagURL: u, FlagToken: tok, Getenv: env(nil), Path: filepath.Join(t.TempDir(), "none")})
			if err == nil {
				t.Fatal("URL with credentials accepted")
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error echoes credentials: %v", err)
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	p, err := DefaultPath(env(map[string]string{"XDG_CONFIG_HOME": "/xdg"}))
	if err != nil || p != "/xdg/sock/config.toml" {
		t.Fatalf("got %q, %v", p, err)
	}
	p, err = DefaultPath(env(map[string]string{"HOME": "/home/u"}))
	if err != nil || p != "/home/u/.config/sock/config.toml" {
		t.Fatalf("got %q, %v", p, err)
	}
}

func TestTokenWhitespaceTrimmed(t *testing.T) {
	p := writeFile(t, 0o600, "token = \" sock_pt_file\\n\"\n")
	cases := map[string]Sources{
		"flag": {FlagURL: "https://x.example", FlagToken: " " + tok + "\n", Getenv: env(nil)},
		"env":  {Getenv: env(map[string]string{"SOCK_URL": "https://x.example", "SOCK_TOKEN": tok + "\r\n"})},
		"file": {Getenv: env(map[string]string{"SOCK_URL": "https://x.example"}), Path: p},
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Load(src)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if strings.TrimSpace(got.Token) != got.Token || !strings.HasPrefix(got.Token, "sock_pt_") {
				t.Fatalf("token %q not trimmed", got.Token)
			}
		})
	}
}

func TestInternalWhitespaceTokenNotEchoed(t *testing.T) {
	_, err := Load(Sources{FlagURL: "https://x.example", FlagToken: "sock_pt_hunter2\nsock_pt_other", Getenv: env(nil)})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("want an error that does not echo the token, got %v", err)
	}
}
