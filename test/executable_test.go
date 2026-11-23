package test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExecutableStartup(t *testing.T) {
	root := t.TempDir()
	configureTestService(t, root)
	data := filepath.Join(root, "runtime-data")
	t.Setenv("ZCODE2API_DATA_DIR", data)

	// CI supplies the artifact from its build step. Local runs build the same main package.
	binary := os.Getenv("ZCODE2API_TEST_BINARY")
	if binary == "" {
		binary = filepath.Join(root, "zcode2api")
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
		build.Dir = ".."
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build executable: %v\n%s", err, output)
		}
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, binary, "--root", root)
	cmd.Dir = root // No src/web directory is available to the running executable.
	var log bytes.Buffer
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = cmd.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("executable exit: %v\n%s", runErr, log.Bytes())
		}
	})
	s := waitForListeners(t, filepath.Join(data, "ports.json"), done)
	checkCtx, stopCheck := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopCheck()
	check := exec.CommandContext(checkCtx, binary, "--root", root, "--healthcheck", "auto")
	check.Dir = root
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("executable health check: %v\n%s", err, output)
	}
	page := request(t, "GET", s.admin+"/", "", "", 200)
	if !bytes.Contains(bytes.ToLower(page), []byte("<html")) {
		t.Fatal("executable did not serve its embedded console")
	}
	s.createKey(t, s.login(t))
}
