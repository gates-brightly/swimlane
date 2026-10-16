package version

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// ModulePath is swim's Go module, as go install names it.
const ModulePath = "github.com/gates-brightly/swimlane"

// ProxyLatest is the version the Go module proxy gives for @latest (what
// `go install ...@latest` installs): the first http(s) entry of GOPROXY,
// proxy.golang.org by default. ok is false when there's no proxy to ask
// (GOPROXY=off or direct), or it didn't answer: the caller then says
// nothing. The proxy caches @latest, so for a while after a release it can
// name an older one.
func ProxyLatest(timeout time.Duration) (v string, ok bool) {
	base := "https://proxy.golang.org"
	if p := os.Getenv("GOPROXY"); p != "" {
		base = ""
		for _, e := range strings.FieldsFunc(p, func(r rune) bool { return r == ',' || r == '|' }) {
			if strings.HasPrefix(e, "http://") || strings.HasPrefix(e, "https://") {
				base = e
				break
			}
		}
		if base == "" {
			return "", false
		}
	}
	c := &http.Client{Timeout: timeout}
	resp, err := c.Get(strings.TrimSuffix(base, "/") + "/" + ModulePath + "/@latest")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var info struct{ Version string }
	if json.NewDecoder(resp.Body).Decode(&info) != nil || info.Version == "" {
		return "", false
	}
	return info.Version, true
}

// ProxyBehind says, when the proxy's @latest (proxied) is an older release
// than tag, what go install @latest would give and the line that installs
// tag instead; "" otherwise.
func ProxyBehind(tag, proxied string) string {
	td, ti, ok1 := ParseTag(tag)
	pd, pi, ok2 := ParseTag(proxied)
	tn, _ := strconv.Atoi(ti)
	pn, _ := strconv.Atoi(pi)
	if !ok1 || !ok2 || pd > td || pd == td && pn >= tn {
		return ""
	}
	return fmt.Sprintf("go install ...@latest still gives %s (the module proxy hasn't caught up with %s): install %s by name:\n    go install %s/cmd/swim@%s\n",
		proxied, tag, tag, ModulePath, tag)
}
