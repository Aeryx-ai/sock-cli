package cli

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// errNotConfirmed is a destructive command the person declined.
var errNotConfirmed = errors.New("not confirmed; nothing changed")

// ask prints prompt to stderr and reads one line from stdin.
func ask(cmd *cobra.Command, prompt string) (string, error) {
	if _, err := fmt.Fprint(cmd.ErrOrStderr(), prompt); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read confirmation: %w", err)
	}
	return strings.TrimSpace(line), nil
}
