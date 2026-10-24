package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/fsutil"
	"github.com/gates-brightly/swimlane/internal/proc"
	"github.com/gates-brightly/swimlane/internal/version"
)

// A Presence is a running swim watch as it announces itself, one file
// under .swim/workers/: which swim it is and the syntaxes it reads, so
// swim queue add can tell, before a batch is queued, whether a worker up
// now can run it. Like a claim, it's alive while its process is (this
// host) or its heartbeat is fresh (another host).
type Presence struct {
	Worker  `yaml:",inline"`
	Version string           `yaml:"version"`
	Syntax  version.Syntaxes `yaml:"syntax"`
	// Builtins are its swim's built-in modules (name: version and hash),
	// to check against .swim.lock's modules: pins.
	Builtins map[string]version.LockedModule `yaml:"builtins,omitempty"`
	// Holding are the batches it leaves queued because it can't run them,
	// each with why.
	Holding map[string]string `yaml:"holding,omitempty"`

	path string
}

// WorkersDir holds the running workers' presence files.
func WorkersDir(root string) string { return filepath.Join(root, ".swim", "workers") }

// An Announcer keeps this process's presence file written while it runs.
type Announcer struct {
	mu   sync.Mutex
	p    Presence
	stop chan struct{}
	done sync.WaitGroup
}

// Announce writes this swim watch's presence file, with its swim's
// built-in modules, and renews its heartbeat every third of lease until
// Stop, which removes it.
func Announce(root, user string, lease time.Duration, builtins map[string]version.LockedModule) *Announcer {
	if lease <= 0 {
		lease = DefaultLease
	}
	pid := os.Getpid()
	a := &Announcer{stop: make(chan struct{}), p: Presence{
		Worker: Worker{Host: hostname(), PID: pid, User: user, Start: proc.Start(pid),
			Claimed: now().UTC().Format(time.RFC3339), Lease: lease.String()},
		Version: version.String(), Syntax: version.Current, Builtins: builtins,
		path: filepath.Join(WorkersDir(root), fmt.Sprintf("%s-%d.yml", hostname(), pid)),
	}}
	os.MkdirAll(WorkersDir(root), 0o755)
	a.write()
	a.done.Add(1)
	go func() {
		defer a.done.Done()
		t := time.NewTicker(lease / 3)
		defer t.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-t.C:
				a.write()
			}
		}
	}()
	return a
}

// Hold records that this worker leaves batch id queued, and why; Hold
// with why "" takes it back. It reports whether that changed anything.
func (a *Announcer) Hold(id, why string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	changed := a.p.Holding[id] != why
	if why == "" {
		delete(a.p.Holding, id)
	} else {
		if a.p.Holding == nil {
			a.p.Holding = map[string]string{}
		}
		a.p.Holding[id] = why
	}
	a.mu.Unlock()
	if changed {
		a.write()
	}
	return changed
}

// Stop ends the heartbeat and removes the presence file.
func (a *Announcer) Stop() {
	if a == nil {
		return
	}
	close(a.stop)
	a.done.Wait()
	os.Remove(a.p.path)
}

func (a *Announcer) write() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.p.Heartbeat = now().UTC().Format(time.RFC3339)
	if data, err := yaml.Marshal(&a.p); err == nil {
		fsutil.WriteAtomic(a.p.path, data, 0o644)
	}
}

// LiveWorkers lists the presences of the workers running now, oldest
// first. A file left by a worker of this host that's gone is removed.
func LiveWorkers(root string) []*Presence {
	files, _ := filepath.Glob(filepath.Join(WorkersDir(root), "*.yml"))
	var out []*Presence
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		p := &Presence{path: f}
		if yaml.Unmarshal(data, p) != nil || p.PID == 0 {
			continue
		}
		if !workerAlive(root, "presence/"+filepath.Base(f), &p.Worker, p.Worker.Heartbeat) {
			if !p.Remote() {
				os.Remove(f)
			}
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Claimed < out[j].Claimed })
	return out
}

// Name is how messages name the worker: "swim watch on mac (pid 412,
// swim v0.20261016.4)".
func (p *Presence) Name() string {
	return fmt.Sprintf("swim watch on %s (pid %d, swim %s)", p.Host, p.PID, p.Version)
}
