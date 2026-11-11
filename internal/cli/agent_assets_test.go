package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// integrations is canonical for the files sock agent install carries. Each
// embedded copy must equal its source at the commit agent/SOURCE records;
// `make agent-assets` refreshes them. Without the sibling checkout (CI) the
// check is skipped.
func TestAgentAssetsMatchIntegrations(t *testing.T) {
	repo := filepath.Join("..", "..", "..", "integrations")
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Skipf("no integrations checkout at %s: embedded runner files and skill not compared", repo)
	}
	commit := strings.TrimSpace(agentSource)
	embedded := map[string]string{
		"runner/sock-runner":          runnerFiles["sock-runner"],
		"runner/lib.sh":               runnerFiles["lib.sh"],
		"agents/skills/sock/SKILL.md": agentSkillMD,
	}
	for path, got := range embedded {
		want, err := exec.Command("git", "-C", repo, "show", commit+":"+path).Output()
		if err != nil {
			t.Fatalf("git show %s:%s in %s: %v (run make agent-assets)", commit, path, repo, err)
		}
		if got != string(want) {
			t.Errorf("embedded %s differs from integrations %s: run make agent-assets", path, commit)
		}
	}
}
