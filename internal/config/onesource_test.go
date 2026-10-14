package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// A token from the file goes only to the file's server: one source, never a
// mix. SOCK_URL from a repo's .envrc must not redirect it.
func TestFileTokenNeverGoesToAnotherURL(t *testing.T) {
	withURL := writeFile(t, 0o600, "url = \"https://file.example\"\naccess_gate = \"fg\"\ntoken = \""+tok+"\"\n")
	noURL := writeFile(t, 0o600, "token = \""+tok+"\"\n")
	for name, src := range map[string]Sources{
		"env url":             {Getenv: env(map[string]string{"SOCK_URL": "https://evil.example"}), Path: withURL},
		"flag url":            {FlagURL: "https://evil.example", Getenv: env(nil), Path: withURL},
		"env url, file none":  {Getenv: env(map[string]string{"SOCK_URL": "https://evil.example"}), Path: noURL},
		"env gate":            {Getenv: env(map[string]string{"SOCK_ACCESS_GATE": "other"}), Path: withURL},
		"env url, file token": {FileTokenOnly: true, Getenv: env(map[string]string{"SOCK_TOKEN": "sock_st_env", "SOCK_URL": "https://evil.example"}), Path: withURL},
	} {
		got, err := Load(src)
		if err == nil || strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), "fg") {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
	}
}

func TestFileTokenTakesTheFileServer(t *testing.T) {
	withURL := writeFile(t, 0o600, "url = \"https://file.example\"\naccess_gate = \"fg\"\ntoken = \""+tok+"\"\n")
	noURL := writeFile(t, 0o600, "token = \""+tok+"\"\n")
	for name, c := range map[string]struct {
		src       Sources
		url, gate string
	}{
		"same env url":     {Sources{FlagURL: "https://file.example", Getenv: env(map[string]string{"SOCK_URL": "https://file.example", "SOCK_ACCESS_GATE": "fg"}), Path: withURL}, "https://file.example", "fg"},
		"default url":      {Sources{Getenv: env(map[string]string{"SOCK_URL": DefaultURL}), Path: noURL}, DefaultURL, ""},
		"file token only":  {Sources{FileTokenOnly: true, FlagToken: "sock_st_flag", Getenv: env(map[string]string{"SOCK_TOKEN": "sock_st_env"}), Path: withURL}, "https://file.example", "fg"},
		"env token mixes":  {Sources{Getenv: env(map[string]string{"SOCK_TOKEN": "sock_st_env", "SOCK_URL": "https://env.example"}), Path: withURL}, "https://env.example", ""},
		"env token, file":  {Sources{Getenv: env(map[string]string{"SOCK_TOKEN": "sock_st_env", "SOCK_URL": "https://file.example"}), Path: withURL}, "https://file.example", "fg"},
		"env token, gate":  {Sources{Getenv: env(map[string]string{"SOCK_TOKEN": "sock_st_env", "SOCK_URL": "https://env.example", "SOCK_ACCESS_GATE": "eg"}), Path: withURL}, "https://env.example", "eg"},
		"anonymous, other": {Sources{Anonymous: true, Getenv: env(map[string]string{"SOCK_URL": "https://env.example"}), Path: withURL}, "https://env.example", ""},
		"same server":      {Sources{Getenv: env(map[string]string{"SOCK_URL": "https://FILE.example/"}), Path: withURL}, "https://file.example", "fg"},
	} {
		got, err := Load(c.src)
		if err != nil || got.URL != c.url || got.AccessGate != c.gate {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
		if name == "anonymous, other" && got.Token != "" {
			t.Errorf("anonymous load paired the file token with another url")
		}
	}
}

// sock login writes the server it signed in at next to the token, so the
// file pairs them; a file naming another server is refused.
func TestSetNewTokenRecordsTheServer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sock", "config.toml")
	if err := SetNewToken(p, "sock_st_dev_secret", "https://staging.example", "g"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(Sources{Getenv: env(map[string]string{"SOCK_URL": "https://staging.example"}), Path: p})
	if err != nil || got.URL != "https://staging.example" || got.AccessGate != "g" || got.Token != "sock_st_dev_secret" {
		t.Fatalf("got %+v, %v", got, err)
	}
	other := writeFile(t, 0o600, "url = \"https://other.example\"\n")
	if err := SetNewToken(other, "sock_st_dev_secret", "https://staging.example", ""); err == nil || strings.Contains(err.Error(), "sock_st_") {
		t.Fatalf("another server: %v", err)
	}
	if tok, _ := FileToken(other); tok != "" {
		t.Fatal("token saved for another server")
	}
}

// Rewriting a malformed file never quotes it: an unquoted gate is the word
// the parser would echo.
func TestRewriteParseErrorHidesContents(t *testing.T) {
	for _, body := range []string{"access_gate = hunter2\n", "hunter2 = = 1\n", "[hunter2]\nx = = 1\n"} {
		p := writeFile(t, 0o600, body)
		if err := SetNewToken(p, "sock_st_dev_secret", DefaultURL, ""); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%q: %v", body, err)
		}
		if err := RemoveToken(p); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%q: %v", body, err)
		}
	}
}

func TestSameServer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"https://x.example", "HTTPS://X.example/", true},
		{"https://x.example/hub", "https://x.example/hub/", true},
		{"https://x.example", "https://y.example", false},
		{"https://x.example", "http://x.example", false},
		{"https://x.example:8443", "https://x.example", false},
		{"https://x.example/a", "https://x.example/b", false},
		{"%zz", "%zz", false},
	} {
		if got := SameServer(c.a, c.b); got != c.want {
			t.Errorf("SameServer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
