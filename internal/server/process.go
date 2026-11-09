package server

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"
)

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
