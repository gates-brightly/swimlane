package hub

import (
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gates-brightly/swimlane/internal/ui"
)

// A namespace groups workers on the dashboard, one tab each: the repo a
// worker runs in, so clones of one repo on different machines sit
// together. A worker says its own at enrollment (swim hub enroll
// --namespace, else NamespaceFor its origin remote) and in every report.
// It only groups the pages: it grants nothing and the hub trusts it no
// more than a worker's name.

// NoNamespace is the tab key for workers that report none (enrolled by a
// swim from before namespaces).
const NoNamespace = "-"

// NamespaceFor is the namespace of the repo at root whose origin remote is
// origin (git's remote.origin.url, "" without one): the remote's host and
// path, without user, port or ".git" ("git@github.com:org/api.git" and
// "https://github.com/org/api" are both "github.com/org/api"). With no
// remote, or a local one, it's the directory's name.
func NamespaceFor(origin, root string) string {
	o := strings.TrimSpace(origin)
	host, p := "", ""
	switch {
	case o == "":
	case strings.Contains(o, "://"):
		if u, err := url.Parse(o); err == nil && u.Scheme != "file" {
			host, p = u.Hostname(), u.Path
		}
	default:
		// scp-like user@host:path; a local path has a slash before any colon.
		if i := strings.Index(o, ":"); i > 0 && !strings.Contains(o[:i], "/") {
			host, p = o[:i], o[i+1:]
			if at := strings.LastIndex(host, "@"); at >= 0 {
				host = host[at+1:]
			}
		}
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || p == "" {
		return CleanNamespace(filepath.Base(root))
	}
	return CleanNamespace(strings.ToLower(host) + "/" + path.Clean(p))
}

// CleanNamespace makes a namespace safe to show and to put in a URL query:
// no controls or bidi overrides (ui.Safe), no surrounding slashes or space,
// at most 200 bytes. "." and NoNamespace aren't namespaces ("").
func CleanNamespace(ns string) string {
	ns = strings.Trim(strings.TrimSpace(ui.Safe(ns)), "/")
	if len(ns) > 200 {
		ns = ns[:200]
	}
	if ns == "." || ns == NoNamespace {
		return ""
	}
	return ns
}

// nsKey is a worker's tab key.
func nsKey(ns string) string {
	if ns == "" {
		return NoNamespace
	}
	return ns
}

// Tab is one namespace on the dashboard.
type Tab struct {
	Key     string // the ?ns= value; "" is every worker
	Label   string
	Workers int
	Running int // workers with a lane running
	Waiting int // workers waiting for an answer or approval
	Active  bool
}

// tabs are "all" and each namespace in views, by name (workers with none
// last), with active the selected key ("" for all).
func tabs(views []View, active string) []Tab {
	all := Tab{Key: "", Label: "all", Active: active == ""}
	by := map[string]*Tab{}
	for _, v := range views {
		k := nsKey(v.Namespace)
		t := by[k]
		if t == nil {
			label := k
			if k == NoNamespace {
				label = "no namespace"
			}
			t = &Tab{Key: k, Label: label, Active: k == active}
			by[k] = t
		}
		for _, c := range []*Tab{t, &all} {
			c.Workers++
			if v.running() {
				c.Running++
			}
			if len(v.Waiting) > 0 || v.Pending() {
				c.Waiting++
			}
		}
	}
	out := []Tab{all}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		if (keys[a] == NoNamespace) != (keys[b] == NoNamespace) {
			return keys[b] == NoNamespace
		}
		return keys[a] < keys[b]
	})
	for _, k := range keys {
		out = append(out, *by[k])
	}
	return out
}

// inNamespace keeps the views in tab key ("" keeps them all).
func inNamespace(views []View, key string) []View {
	if key == "" {
		return views
	}
	var out []View
	for _, v := range views {
		if nsKey(v.Namespace) == key {
			out = append(out, v)
		}
	}
	return out
}

func (v View) running() bool {
	for _, l := range v.Lanes {
		if strings.HasPrefix(l.State, "running") {
			return true
		}
	}
	return false
}
