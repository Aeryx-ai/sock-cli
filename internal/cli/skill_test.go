package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillInstallWritesEmbeddedSkill(t *testing.T) {
	dir := t.TempDir()
	out, _, err := run(t, DefaultApp("test"), nil, "skill", "install", "--location", dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	path := filepath.Join(dir, "sock", "SKILL.md")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != skillMD {
		t.Fatalf("installed skill differs from the embedded one")
	}
	if !strings.Contains(out, path) {
		t.Fatalf("output %q does not name %s", out, path)
	}
}

func TestSkillInstallDefaultsToAgentsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if _, _, err := run(t, DefaultApp("test"), nil, "skill", "install"); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "sock", "SKILL.md")); err != nil {
		t.Fatalf("default location: %v", err)
	}
}

func TestSkillInstallReplacesOlderCopy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sock", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, DefaultApp("test"), nil, "skill", "install", "--location", dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != skillMD {
		t.Fatalf("old copy not replaced")
	}
}

func TestSkillRemoveDeletesSkillAndEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := run(t, DefaultApp("test"), nil, "skill", "install", "--location", dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, _, err := run(t, DefaultApp("test"), nil, "skill", "remove", "--location", dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sock")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sock dir left behind: %v", err)
	}
}

func TestSkillRemoveKeepsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := run(t, DefaultApp("test"), nil, "skill", "install", "--location", dir); err != nil {
		t.Fatalf("install: %v", err)
	}
	mine := filepath.Join(dir, "sock", "notes.md")
	if err := os.WriteFile(mine, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, DefaultApp("test"), nil, "skill", "remove", "--location", dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("removed a file sock did not write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sock", "SKILL.md")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SKILL.md left behind: %v", err)
	}
}

func TestSkillRemoveWhenNotInstalled(t *testing.T) {
	out, _, err := run(t, DefaultApp("test"), nil, "skill", "remove", "--location", t.TempDir())
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "not installed") {
		t.Fatalf("output %q", out)
	}
}

func TestSkillFrontmatterNamesSock(t *testing.T) {
	if !strings.HasPrefix(skillMD, "---\nname: sock\ndescription: ") {
		t.Fatalf("SKILL.md frontmatter must start with name: sock and a description")
	}
}
