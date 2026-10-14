package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListenPolicy(t *testing.T) {
	for _, c := range []struct {
		addr          string
		tls, insecure bool
		ok            bool
	}{
		{"127.0.0.1:7700", false, false, true},
		{"localhost:7700", false, false, true},
		{"[::1]:7700", false, false, true},
		{"127.0.0.2:0", false, false, true},
		{"0.0.0.0:7700", false, false, false},
		{":7700", false, false, false},
		{"[::]:7700", false, false, false},
		{"10.1.2.3:7700", false, false, false},
		{"hub.example.com:7700", false, false, false},
		{"0.0.0.0:7700", true, false, true},
		{"10.1.2.3:7700", true, false, true},
		{"0.0.0.0:7700", false, true, true},
		{":7700", false, true, true},
		{"no-port", false, false, false},
		{"no-port", true, true, false},
	} {
		err := listenPolicy(c.addr, c.tls, c.insecure)
		if (err == nil) != c.ok {
			t.Errorf("listenPolicy(%q, tls=%v, insecure=%v) = %v, want ok=%v", c.addr, c.tls, c.insecure, err, c.ok)
		}
		var ue usageError
		if err != nil && !errors.As(err, &ue) {
			t.Errorf("listenPolicy(%q): %v is not a usage error (exit 2)", c.addr, err)
		}
	}
}

// writeCert writes a fresh self-signed certificate for cn to dir.
func writeCert(t *testing.T, certFile, keyFile, cn string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{cn}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
}

func TestCertReloader(t *testing.T) {
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	writeCert(t, cf, kf, "one")
	old := time.Now().Add(-time.Minute)
	os.Chtimes(cf, old, old)
	os.Chtimes(kf, old, old)
	r, err := newCertReloader(cf, kf)
	if err != nil {
		t.Fatal(err)
	}
	cn := func() string {
		c, _ := r.get(nil)
		x, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return x.Subject.CommonName
	}
	if got := cn(); got != "one" {
		t.Fatalf("first: %s", got)
	}
	writeCert(t, cf, kf, "two")
	if got := cn(); got != "two" {
		t.Fatalf("after renewal: %s, want two", got)
	}
	// A renewal that doesn't load keeps serving the last good one.
	os.WriteFile(cf, []byte("garbage"), 0o600)
	if got := cn(); got != "two" {
		t.Fatalf("after a bad renewal: %s, want two", got)
	}
	if _, err := newCertReloader(cf, kf); err == nil {
		t.Fatal("a bad certificate loads at start")
	}
}
