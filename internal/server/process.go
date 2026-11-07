package server

import "errors"

// Bound stdout from the Node.js and Python helpers before decoding their JSON.
type limitedProcessOutput struct{ value []byte }

func (w *limitedProcessOutput) Write(p []byte) (int, error) {
	if len(w.value)+len(p) > 64<<10 {
		return 0, errors.New("process output exceeds 64 KiB")
	}
	w.value = append(w.value, p...)
	return len(p), nil
}
