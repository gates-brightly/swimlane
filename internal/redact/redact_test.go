package redact

import (
	"bytes"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

func TestMaskerValuesAndForms(t *testing.T) {
	tok := "ghp_testvalue123456"
	env := []string{"GITHUB_TOKEN=" + tok, "TOKEN_COUNT=5", "MY_PASSWORD=hunter22!", "SHORT_TOKEN=abc", "PLAIN=visible-value", "DD=dd-key-0001", "PEM_PRIVATE_KEY=-----BEGIN-----\nlinetwo-secret\n-----END-----"}
	m := New(env, Options{Names: []string{"DD"}, Auto: true})
	if got := strings.Join(m.Names(), ","); got != "DD,GITHUB_TOKEN,MY_PASSWORD,PEM_PRIVATE_KEY" {
		t.Fatalf("names = %q (SHORT_TOKEN is too short, PLAIN and TOKEN_COUNT don't match)", got)
	}
	in := strings.Join([]string{
		"token " + tok,
		"b64 " + base64.StdEncoding.EncodeToString([]byte(tok)),
		"url ?p=" + url.QueryEscape("hunter22!"),
		"pem linetwo-secret",
		"listed dd-key-0001",
		"plain visible-value abc 5",
	}, "\n")
	out := m.String(in)
	for _, raw := range []string{tok, base64.StdEncoding.EncodeToString([]byte(tok)), url.QueryEscape("hunter22!"), "linetwo-secret", "dd-key-0001"} {
		if strings.Contains(out, raw) {
			t.Errorf("%q survived:\n%s", raw, out)
		}
	}
	if !strings.Contains(out, "plain visible-value abc 5") {
		t.Errorf("non-secrets were masked:\n%s", out)
	}
	if m2 := New(env, Options{Auto: true, Ignore: []string{"MY_PASSWORD"}}); m2.Value("MY_PASSWORD") {
		t.Error("secret_env_ignore not honoured")
	}
}

func TestMaskerOverlappingValuesLongestFirst(t *testing.T) {
	m := New([]string{"A_TOKEN=abcdefgh", "B_TOKEN=abcdefghijkl"}, Options{Auto: true})
	if got := m.String("x abcdefghijkl y"); got != "x *** y" {
		t.Fatalf("got %q (the longer value must be masked whole)", got)
	}
}

func TestValuePatterns(t *testing.T) {
	m := New(nil, Options{AutoPatterns: true})
	in := "aws AKIAIOSFODNN7EXAMPLE gh ghp_" + strings.Repeat("a", 36) + " jwt eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2QT4 slack xoxb-1234567890-abc"
	out := m.String(in)
	if strings.Contains(out, "AKIA") || strings.Contains(out, "ghp_") || strings.Contains(out, "eyJ") || strings.Contains(out, "xoxb") {
		t.Fatalf("patterns not masked: %s", out)
	}
	if New(nil, Options{}).Active() {
		t.Error("nothing to mask should be inactive")
	}
}

func TestWriterSplitAcrossWrites(t *testing.T) {
	tok := "s3cr3t-value-xyz"
	m := New([]string{"API_TOKEN=" + tok}, Options{Auto: true})
	var b bytes.Buffer
	w := m.Writer(&b)
	text := "before " + tok + " after\nprogress " + tok + "\rnext line no newline " + tok
	for i := 0; i < len(text); i++ { // one byte at a time
		w.Write([]byte{text[i]})
	}
	w.Flush()
	if strings.Contains(b.String(), tok) || strings.Count(b.String(), Mask) != 3 {
		t.Fatalf("got %q", b.String())
	}
}
