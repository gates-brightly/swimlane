package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/install.sh, the release's installer: it fetches this machine's
// archive from a release laid out as GitHub's, checks it against
// checksums.txt and installs swim; "latest" is resolved to its tag first; a
// download whose checksum doesn't match installs nothing.
func TestInstallScript(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("needs curl")
	}
	const tag = "v0.20261102.1"
	asset := fmt.Sprintf("swim_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarGz(t, "swim", "#!/bin/sh\necho 'swim 0.20261102.1 (test)'\n")
	sum := sha256.Sum256(archive)
	checksums := hex.EncodeToString(sum[:]) + "  " + asset + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest": // GitHub redirects to the newest release's tag page
			http.Redirect(w, r, "/tag/"+tag, http.StatusFound)
		case "/tag/" + tag:
			fmt.Fprintln(w, "release page")
		case "/download/" + tag + "/" + asset:
			w.Write(archive)
		case "/download/" + tag + "/checksums.txt":
			fmt.Fprint(w, checksums)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	install := func(env ...string) (string, string, error) {
		t.Helper()
		dir := t.TempDir()
		c := exec.Command("sh", filepath.Join(repoRoot(), "scripts", "install.sh"))
		c.Env = append(os.Environ(), "SWIM_DOWNLOAD_BASE="+srv.URL, "SWIM_INSTALL_DIR="+dir)
		c.Env = append(c.Env, env...)
		out, err := c.CombinedOutput()
		return string(out), dir, err
	}

	for _, env := range [][]string{{"SWIM_VERSION=" + tag}, {"SWIM_VERSION="}} {
		out, dir, err := install(env...)
		if err != nil || !strings.Contains(out, "installed swim 0.20261102.1 (test) at "+dir+"/swim") {
			t.Fatalf("%v: %v\n%s", env, err, out)
		}
		if fi, err := os.Stat(filepath.Join(dir, "swim")); err != nil || fi.Mode()&0o111 == 0 {
			t.Errorf("%v: swim isn't installed executable: %v", env, err)
		}
	}

	checksums = strings.Repeat("0", 64) + "  " + asset + "\n"
	out, dir, err := install("SWIM_VERSION=" + tag)
	if err == nil || !strings.Contains(out, "checksum") || !strings.Contains(out, "not installed") {
		t.Fatalf("a bad checksum: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "swim")); err == nil {
		t.Error("a download that failed its checksum was installed")
	}
}

// tarGz is a .tar.gz holding one executable file.
func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return b.Bytes()
}
