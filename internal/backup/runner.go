package backup

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Runner abstracts shelling out to external binaries so tests can substitute a
// fake. The default implementation just wraps os/exec.
type Runner interface {
	Run(ctx context.Context, name string, env []string, args ...string) (stdout []byte, stderr []byte, err error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, env []string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), stderr.Bytes(), nil
}
