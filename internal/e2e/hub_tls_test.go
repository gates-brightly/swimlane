package e2e

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// selfSigned writes a self-signed certificate (and key) for 127.0.0.1.
func selfSigned(t *testing.T, dir string) (certFile, keyFile string, pemData []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "swim hub test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	certFile, keyFile = filepath.Join(dir, "hub.crt"), filepath.Join(dir, "hub.key")
	pemData = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	noErr(t, os.WriteFile(certFile, pemData, 0o600))
	noErr(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600))
	return certFile, keyFile, pemData
}

func TestHubTLS(t *testing.T) {
	r := newRepo(t, "")
	certFile, keyFile, caPEM := selfSigned(t, t.TempDir())
	hub := exec.Command(bin, "hub", "--addr", "127.0.0.1:0", "--token", "t0ken", "--tls-cert", certFile, "--tls-key", keyFile, "--no-tui")
	hub.Env = r.env
	hub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, _ := hub.StdoutPipe()
	if err := hub.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { syscall.Kill(-hub.Process.Pid, syscall.SIGTERM); hub.Wait() }()
	sc := bufio.NewScanner(out)
	sc.Scan()
	url := regexp.MustCompile(`https://[0-9.:]+`).FindString(sc.Text())
	if url == "" {
		t.Fatalf("no https hub URL in %q", sc.Text())
	}
	go func() {
		for sc.Scan() {
		}
	}()

	// Without --ca the hub's self-signed certificate is unknown.
	if out, code := r.swim("hub", "enroll", url, "--token", "t0ken"); code == 0 || !strings.Contains(out, "unknown authority") || !strings.Contains(out, "--ca") {
		t.Fatalf("enroll without --ca: %d %s", code, out)
	}
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	noErr(t, os.WriteFile(caFile, caPEM, 0o600))
	contains(t, "enroll --ca", r.mustSwim("hub", "enroll", url, "--token", "t0ken", "--name", "billing", "--ca", caFile), "enrolled", "as billing")
	// The CA is kept with the enrollment: later reports trust it too.
	noErr(t, os.Remove(caFile))
	contains(t, "status", r.mustSwim("hub", "status"), "enrolled with "+url+" as billing", "reachable")

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := c.Get(url + "/api/enrollments")
	if err != nil {
		t.Fatal(err)
	}
	var w hubWorkers
	json.NewDecoder(resp.Body).Decode(&w)
	resp.Body.Close()
	if len(w.Workers) != 1 || w.Workers[0].Name != "billing" || w.Workers[0].Activity != "checked by swim hub status" {
		t.Fatalf("over https: %+v", w)
	}
	if out, _ := r.swim("doctor"); strings.Contains(out, "hub-http") {
		t.Errorf("doctor warns about an https hub:\n%s", out)
	}
}

func TestHubTLSPolicy(t *testing.T) {
	r := newRepo(t, "")
	// Every interface without TLS: refused before it binds.
	out, code := r.swim("hub", "--addr", "0.0.0.0:0", "--token", "t", "--no-tui")
	if code != 2 || !strings.Contains(out, "plain HTTP") || strings.Contains(out, "swim hub · ") {
		t.Fatalf("hub on 0.0.0.0 without TLS: %d %s", code, out)
	}
	if out, code := r.swim("hub", "--tls-cert", "x.pem", "--no-tui"); code != 2 || !strings.Contains(out, "go together") {
		t.Fatalf("--tls-cert alone: %d %s", code, out)
	}
	// Plain http to another machine: refused with no network call (the
	// address is unroutable in tests; a dial would take seconds, not fail fast).
	start := time.Now()
	out, code = r.swim("hub", "enroll", "http://10.1.2.3:7700", "--token", "t")
	if code != 2 || !strings.Contains(out, "plain http") || !strings.Contains(out, "--insecure") {
		t.Fatalf("enroll http to another machine: %d %s", code, out)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("the refusal took %v: it tried the network", time.Since(start))
	}
	if out, code := r.swim("hub", "enroll", "http://127.0.0.1:1", "--token", "t", "--ca", "x.pem"); code != 2 || !strings.Contains(out, "--ca is for an https") {
		t.Fatalf("--ca with http: %d %s", code, out)
	}
	if out, code := r.swim("hub", "login", "http://10.1.2.3:7700", "--admin-token", "a"); code != 2 || !strings.Contains(out, "plain http") {
		t.Fatalf("login http to another machine: %d %s", code, out)
	}

	// An existing plain-http enrollment isn't refused (swim watch keeps
	// reporting with it), but doctor warns.
	r.write(".swim/hub.yml", "url: http://10.1.2.3:7700\ntoken: t\nid: x\nname: n\n", 0o600)
	contains(t, "doctor", must(r.swim("doctor")), "[hub-http]", "http://10.1.2.3:7700")
}
