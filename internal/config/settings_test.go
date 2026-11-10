package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	client "github.com/aeryx-ai/sock-client"
)

func TestSetMessageOwnerKeepsOtherKeys(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sock", "config.toml")
	if err := SetMachineKey(p, "sock_mk_keep_secret", "https://hub.example", "", false); err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		if err := SetMessageOwner(p, on); err != nil {
			t.Fatal(err)
		}
		f, err := client.LoadFileConfig(p)
		if err != nil || f.MessageOwner == nil || *f.MessageOwner != on || f.MachineKey != "sock_mk_keep_secret" || f.URL != "https://hub.example" {
			t.Fatalf("on %v: %+v, %v", on, f, err)
		}
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", fi.Mode(), err)
	}
}

func TestSetURLWritesTheServer(t *testing.T) {
	p := writeFile(t, 0o600, "message_owner = false\n")
	if err := SetURL(p, "https://hub.example"); err != nil {
		t.Fatal(err)
	}
	f, err := client.LoadFileConfig(p)
	if err != nil || f.URL != "https://hub.example" || f.MessageOwner == nil || *f.MessageOwner {
		t.Fatalf("%+v, %v", f, err)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

// A credential is never sent to a server it was not issued by: url changes
// only while the file holds none.
func TestSetURLRefusesWhileACredentialIsPresent(t *testing.T) {
	for _, tc := range []struct{ keys, want string }{
		{"token", "the config file's token belongs to https://old.example; remove it from the file first (sock logout removes token)"},
		{"puppet_token", "the config file's puppet_token belongs to https://old.example; remove it from the file first"},
		{"machine_key", "the config file's machine_key belongs to https://old.example; remove it from the file first"},
		{"token machine_key", "the config file's token and machine_key belong to https://old.example; remove them from the file first (sock logout removes token)"},
		{"token puppet_token machine_key", "the config file's token, puppet_token and machine_key belong to https://old.example; remove them from the file first (sock logout removes token)"},
	} {
		body := "url = \"https://old.example\"\n"
		for _, k := range strings.Fields(tc.keys) {
			body += k + " = \"sock_xx_secret\"\n"
		}
		p := writeFile(t, 0o600, body)
		err := SetURL(p, "https://new.example")
		if !errors.Is(err, ErrCredentialsPresent) || err == nil || err.Error() != tc.want {
			t.Fatalf("%s: %v", tc.keys, err)
		}
		key := tc.keys
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("%s: file changed to %q", key, b)
		}
	}
	// no url in the file: the credentials belong to the default server
	p := writeFile(t, 0o600, "machine_key = \"sock_mk_secret\"\n")
	if err := SetURL(p, "https://new.example"); err == nil || !strings.Contains(err.Error(), "belongs to "+DefaultURL+";") {
		t.Fatalf("got %v", err)
	}
}

// The file's access gate goes only to the file's url, so the url does not
// move to another server under it.
func TestSetURLRefusesAnotherServerUnderAnAccessGate(t *testing.T) {
	body := "url = \"https://old.example\"\naccess_gate = \"g\"\n"
	p := writeFile(t, 0o600, body)
	if err := SetURL(p, "https://new.example"); err == nil || strings.Contains(err.Error(), "\"g\"") {
		t.Fatalf("got %v", err)
	}
	if err := SetURL(p, "https://OLD.example/"); err != nil {
		t.Fatalf("same server refused: %v", err)
	}
}

func TestSetURLRefusesABadURL(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	for _, bad := range []string{"", "ftp://hub.example", "not a url", "https://user:pw@hub.example"} {
		if err := SetURL(p, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a refused url wrote the file")
	}
}

// A token is removed only while the file still holds the one checked: a
// newer one written since is kept.
func TestRemoveCheckedToken(t *testing.T) {
	body := "url = \"https://hub.example\"\ntoken = \"sock_st_newer_secret\"\n"
	p := writeFile(t, 0o600, body)
	if err := RemoveCheckedToken(p, "sock_st_checked_secret"); !errors.Is(err, ErrTokenChanged) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatalf("file changed to %q", b)
	}
	if err := RemoveCheckedToken(p, "sock_st_newer_secret"); err != nil {
		t.Fatal(err)
	}
	if f, _ := client.LoadFileConfig(p); f.Token != "" || f.URL != "https://hub.example" {
		t.Fatalf("%+v", f)
	}
}

// A machine key is replaced only while the file still holds the rejected
// one: a newer one written since is kept.
func TestReplaceMachineKey(t *testing.T) {
	body := "url = \"https://hub.example\"\nmachine_key = \"sock_mk_newer_secret\"\n"
	p := writeFile(t, 0o600, body)
	if err := ReplaceMachineKey(p, "sock_mk_dead_secret", "sock_mk_minted_secret", "https://hub.example", ""); !errors.Is(err, ErrMachineKeyExists) {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != body {
		t.Fatalf("file changed to %q", b)
	}
	if err := ReplaceMachineKey(p, "sock_mk_newer_secret", "sock_mk_minted_secret", "https://hub.example", ""); err != nil {
		t.Fatal(err)
	}
	if f, _ := client.LoadFileConfig(p); f.MachineKey != "sock_mk_minted_secret" {
		t.Fatalf("%+v", f)
	}
}
