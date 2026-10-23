package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/lane"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/module"
	"github.com/gates-brightly/swimlane/internal/version"
)

// stageLanes builds each selected lane's execution manifest and stages its
// files (.swim/runs/<run>/<lane>/), before any lane waits for anything:
// what a lane runs is fixed now, whatever happens to the working tree while
// it waits for its dependencies, locks or a slot. A lane that can't be
// staged, or whose staged script holds a blocked command, is refused; why,
// per lane, goes in blocked.
func stageLanes(o *Options, sel []int, blocked map[int]string) {
	o.manifests = map[int]*manifest.Manifest{}
	for _, n := range sel {
		if blocked[n] != "" {
			continue
		}
		envSHA := ""
		if r := o.env[n]; r != nil {
			envSHA = r.SHA256()
		}
		m, err := manifest.Build(o.Root, o.Cfg, n, envSHA)
		if err == nil {
			// Its built-in modules as .swim.lock pins them (swim modules lock).
			if why := lockedModules(o.Root, m); why != "" {
				blocked[n] = "blocked: " + why
				continue
			}
			_, err = m.Stage(o.Root, o.RunID)
		}
		if err != nil {
			blocked[n] = "blocked: can't stage the lane's files: " + err.Error()
			continue
		}
		o.manifests[n] = m
		if why := blockedIn(o.Root, o.Cfg, n, m.Script()); why != "" {
			blocked[n] = why
		}
	}
}

// pinned says why lane n mustn't start ("" if it may): its staged files
// changed since they were staged, or what it holds isn't what a queued
// batch pinned (the script, its manifest, its env).
func pinned(o Options, n int) (why string, refuse bool) {
	m := o.manifests[n]
	if m == nil {
		return "", false
	}
	if err := m.Verify(); err != nil {
		return "blocked: " + err.Error(), true
	}
	if want := o.PinSHA[n]; want != "" && m.ScriptSHA != want {
		return fmt.Sprintf("%s changed since it was queued (queued %s)", lane.ScriptName(o.Root, n), want[:min(12, len(want))]), false
	}
	if want := o.PinManifest[n]; want != "" && !m.Matches(want) {
		return fmt.Sprintf("its manifest changed since it was queued (queued %s, now %s): its includes, sidecar, runner or swim.yml policy", want[:min(12, len(want))], m.SHA256[:12]), false
	}
	if want, ok := o.PinEnv[n]; ok && want != m.Local.Env {
		was := "no env"
		if len(want) >= 12 {
			was = want[:12]
		}
		return fmt.Sprintf("its env files changed since it was queued (queued %s)", was), false
	}
	return "", false
}

// stageEnv is the environment that tells lane n's process where its stage
// and manifest are, and its approval token (registered with the facts the
// question is built from: the lane, cell, job, run and manifest, and the
// process once it starts, see started), which it returns too.
func stageEnv(o Options, n int, who approval.Caller) ([]string, string) {
	m := o.manifests[n]
	if m == nil {
		return nil, ""
	}
	env := []string{manifest.StageEnv + "=" + m.Dir, manifest.ManifestEnv + "=" + filepath.Join(m.Dir, manifest.FileName), "SWIM_ROOT=" + o.Root}
	tok := ""
	if o.approvalSrv != nil {
		who.Lane, who.Run, who.Manifest, who.Guards, who.AnyGuard, who.PID = n, o.RunID, m.SHA256, m.Guards, m.DynamicGuards, approval.AwaitPID
		tok = o.approvalSrv.Register(who)
		env = append(env, approval.TokenEnv+"="+tok)
	}
	return env, tok
}

// started binds a lane process's approval token to its pid: only it and
// its descendants may use the token.
func started(o Options, tok string, pid int) {
	if o.approvalSrv != nil && tok != "" {
		o.approvalSrv.SetPID(tok, pid)
	}
}

// exited revokes a lane process's token: nothing it left behind can ask
// or report with it.
func exited(o Options, tok string) {
	if o.approvalSrv != nil && tok != "" {
		o.approvalSrv.Revoke(tok)
		o.auth.forget(tok)
	}
}

// probesFor are the Observe: probes of lane n (one cell or job of it), from
// its staged files.
func probesFor(o Options, n int, cell string) []lane.Probe {
	m := o.manifests[n]
	if m == nil {
		return nil
	}
	data, err := os.ReadFile(m.Script())
	if err != nil {
		return nil
	}
	side := ""
	if m.Pinned.Sidecar != nil {
		side = m.Path(m.Pinned.Sidecar.Path)
	}
	return lane.ProbesIn(filepath.Base(m.Pinned.Script.Path), data, n, side, cell)
}

// lockedModules says why m's built-in modules aren't the ones the repo's
// .swim.lock pins ("" if they are, or the lock pins none).
func lockedModules(root string, m *manifest.Manifest) string {
	lock, ok, err := version.ReadLock(root)
	if err != nil || !ok || lock.Modules == nil {
		return ""
	}
	var whys []string
	for _, p := range m.Pinned.Modules {
		if p.Source != module.BuiltinSource {
			continue
		}
		if why := module.PinProblem(lock, p.Name, p.Version, p.SHA256); why != "" {
			whys = append(whys, why)
		}
	}
	return strings.Join(whys, "; ")
}
