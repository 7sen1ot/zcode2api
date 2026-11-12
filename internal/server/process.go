package server

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Explicit paths are relative to the project root; bare names use PATH first.
func resolveExecutable(root, binary string, fallbacks ...string) (string, error) {
	if !filepath.IsAbs(binary) && strings.ContainsAny(binary, "/\\") {
		binary = filepath.Join(root, binary)
	}
	resolved, err := exec.LookPath(binary)
	for _, candidate := range fallbacks {
		if err == nil {
			break
		}
		resolved, err = exec.LookPath(candidate)
	}
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func newJSONCommand(ctx context.Context, binary string, input M, args ...string) (*exec.Cmd, *limitedProcessOutput) {
	command := exec.CommandContext(ctx, binary, args...)
	hideChildWindow(command)
	command.Stdin = strings.NewReader(js(input))
	output := &limitedProcessOutput{}
	command.Stdout, command.Stderr = output, io.Discard
	command.WaitDelay = time.Second
	return command, output
}

// Bound stdout from the Node.js and Python helpers before decoding their JSON.
type limitedProcessOutput struct{ value []byte }

func (w *limitedProcessOutput) Write(p []byte) (int, error) {
	if len(w.value)+len(p) > 64<<10 {
		return 0, errors.New("process output exceeds 64 KiB")
	}
	w.value = append(w.value, p...)
	return len(p), nil
}
