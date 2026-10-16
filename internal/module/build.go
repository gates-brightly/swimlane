package module

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BuildGo builds a repo Go module from src (its staged directory) into
// .swim/modules/build/<tree sha>-<go version>-<goos>-<goarch>, once per
// source and toolchain: with the operator's Go, offline (GOPROXY=off:
// vendor what it imports). It returns the binary and the Go version that
// built it, and says on log when it builds.
func BuildGo(root string, m *Module, src string, log io.Writer) (bin, goVersion string, err error) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return "", "", fmt.Errorf("module %s is a Go module: building it needs a Go toolchain on PATH", m.Name)
	}
	ver, err := exec.Command(gobin, "env", "GOVERSION").Output()
	if err != nil {
		return "", "", fmt.Errorf("module %s: go env GOVERSION: %w", m.Name, err)
	}
	goVersion = strings.TrimSpace(string(ver))
	dir := filepath.Join(root, ".swim", "modules", "build")
	bin = filepath.Join(dir, fmt.Sprintf("%s-%s-%s-%s", m.SHA256[:32], goVersion, runtime.GOOS, runtime.GOARCH))
	if st, err := os.Stat(bin); err == nil && st.Mode().IsRegular() {
		return bin, goVersion, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", bin, os.Getpid())
	c := exec.Command(gobin, "build", "-trimpath", "-o", tmp, ".")
	c.Dir = src
	c.Env = append(os.Environ(), "GOPROXY=off", "GOFLAGS=", "GOWORK=off")
	var errb bytes.Buffer
	c.Stdout, c.Stderr = &errb, &errb
	if log != nil {
		fmt.Fprintf(log, "%s: building ./%s (%s)\n", m.Name, m.Dir, m.SHA256[:12])
	}
	if err := c.Run(); err != nil {
		os.Remove(tmp)
		return "", "", fmt.Errorf("module %s: go build: %s", m.Name, strings.TrimSpace(errb.String()))
	}
	return bin, goVersion, os.Rename(tmp, bin)
}
