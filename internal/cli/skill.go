package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// skillMD is the agent skill sock skill install writes: it tells a model in
// a host such as OpenHands that the sock MCP tools are connected.
//
//go:embed skill/SKILL.md
var skillMD string

// skillName is the skill's directory under the skills location.
const skillName = "sock"

func newSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install or remove the sock agent skill",
	}
	var location string
	cmd.PersistentFlags().StringVar(&location, "location", "", "skills directory (default ~/.agents/skills)")
	dir := func() (string, error) {
		if location != "" {
			return filepath.Join(location, skillName), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no home directory for the default location; pass --location: %w", err)
		}
		return filepath.Join(home, ".agents", "skills", skillName), nil
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Write the skill to <location>/sock/SKILL.md, replacing an older copy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
			if err != nil {
				return err
			}
			path, err := installSkill(d)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", path)
			return err
		},
	}, &cobra.Command{
		Use:   "remove",
		Short: "Remove <location>/sock/SKILL.md, and the directory when nothing else is in it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
			if err != nil {
				return err
			}
			path := filepath.Join(d, "SKILL.md")
			switch err := os.Remove(path); {
			case errors.Is(err, fs.ErrNotExist):
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "not installed at %s\n", path)
				return err
			case err != nil:
				return fmt.Errorf("remove skill: %w", err)
			}
			// a directory holding files sock did not write stays
			_ = os.Remove(d)
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", path)
			return err
		},
	})
	return cmd
}

// installSkill writes SKILL.md into dir through a temporary file and a
// rename, so a reader never sees half a skill.
func installSkill(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("install skill: %w", err)
	}
	f, err := os.CreateTemp(dir, ".SKILL.md.*")
	if err != nil {
		return "", fmt.Errorf("install skill: %w", err)
	}
	defer func() { _ = os.Remove(f.Name()) }() // no-op after the rename
	_, werr := f.WriteString(skillMD)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(f.Name(), 0o644)
	}
	path := filepath.Join(dir, "SKILL.md")
	if werr == nil {
		werr = os.Rename(f.Name(), path)
	}
	if werr != nil {
		return "", fmt.Errorf("install skill: %w", werr)
	}
	return path, nil
}
