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
	for _, key := range []string{"token", "puppet_token", "machine_key"} {
		body := "url = \"https://old.example\"\n" + key + " = \"sock_xx_secret\"\n"
		p := writeFile(t, 0o600, body)
		err := SetURL(p, "https://new.example")
		if !errors.Is(err, ErrCredentialsPresent) || err.Error() != "sign out first (sock logout); the credentials here belong to https://old.example" {
			t.Fatalf("%s: %v", key, err)
		}
		if b, _ := os.ReadFile(p); string(b) != body {
			t.Fatalf("%s: file changed to %q", key, b)
		}
	}
	// no url in the file: the credentials belong to the default server
	p := writeFile(t, 0o600, "machine_key = \"sock_mk_secret\"\n")
	if err := SetURL(p, "https://new.example"); err == nil || !strings.HasSuffix(err.Error(), "belong to "+DefaultURL) {
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
