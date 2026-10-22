package cli

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gates-brightly/swimlane/internal/hub"
)

// listenPolicy decides, before anything binds, whether swim hub may serve
// on addr: plain HTTP only on a loopback address unless insecure. Every
// interface ("", 0.0.0.0, ::) and any other host count as the network.
func listenPolicy(addr string, useTLS, insecure bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return usagef("--addr %s: %v (want HOST:PORT)", addr, err)
	}
	if useTLS || insecure || hub.LoopbackHost(host) {
		return nil
	}
	return usagef("swim hub --addr %s would serve plain HTTP to the network: tokens, logs and secrets would cross it unencrypted\n"+
		"  serve TLS: --tls-cert FILE --tls-key FILE\n"+
		"  or keep the hub on 127.0.0.1 behind a TLS proxy\n"+
		"  or pass --insecure (a trusted network only)", addr)
}

// checkPublicURL is --public-url made canonical: http or https, a host,
// no path (the hub serves from its root), no query, no trailing slash.
func checkPublicURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", usagef("--public-url %s: want http(s)://HOST[:PORT], the address clients reach the hub at", raw)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", usagef("--public-url %s: the hub serves from its root: give the scheme and host only", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// certReloader serves the certificate in certFile/keyFile, loading it again
// when either file's mtime changes, so a renewed certificate takes effect
// without a restart. A renewal that doesn't load keeps the old one.
type certReloader struct {
	certFile, keyFile string
	mu                sync.Mutex
	cert              *tls.Certificate
	mod               time.Time
}

func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	if err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// modTime is the later of the two files' mtimes.
func (r *certReloader) modTime() time.Time {
	var t time.Time
	for _, f := range []string{r.certFile, r.keyFile} {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
	}
	return t
}

func (r *certReloader) load() error {
	mod := r.modTime()
	c, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return fmt.Errorf("--tls-cert/--tls-key: %w", err)
	}
	r.cert, r.mod = &c, mod
	return nil
}

func (r *certReloader) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.modTime().Equal(r.mod) {
		if err := r.load(); err != nil {
			fmt.Fprintln(os.Stderr, "swim hub: keeping the old certificate: "+err.Error())
			r.mod = r.modTime() // don't retry on every handshake until it changes again
		}
	}
	return r.cert, nil
}

// tlsConfig is the hub's TLS: 1.2 at least, the certificate from r.
func (r *certReloader) tlsConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.get}
}
