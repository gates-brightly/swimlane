package module

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	awssession "github.com/gates-brightly/swimlane/internal/module/builtin/aws-session"
	"github.com/gates-brightly/swimlane/internal/module/builtin/backup"
	"github.com/gates-brightly/swimlane/internal/module/builtin/claude"
	"github.com/gates-brightly/swimlane/internal/module/builtin/smoke"
	"github.com/gates-brightly/swimlane/internal/module/builtin/target"
	"github.com/gates-brightly/swimlane/internal/module/builtin/tf"
	"github.com/gates-brightly/swimlane/internal/module/builtin/wait"
	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

// The built-in modules' files: their module.yml, and their source (a Go
// module's too, so its hash changes when its code does).
//
//go:embed builtin
var builtinFS embed.FS

// goBuiltins are the built-in Go modules, compiled into swim.
var goBuiltins = map[string]func(*gomod.Ctx) error{
	"aws-session": awssession.Run,
	"backup":      backup.Run,
	"claude":      claude.Run,
	"smoke":       smoke.Run,
	"target":      target.Run,
	"tf":          tf.Run,
	"wait":        wait.Run,
}

// GoBuiltin is a built-in Go module's code (nil if name isn't one).
func GoBuiltin(name string) func(*gomod.Ctx) error { return goBuiltins[name] }

var (
	builtinOnce sync.Once
	builtins    map[string]*Module
)

func loadBuiltins() {
	builtins = map[string]*Module{}
	dirs, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		panic(err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files := map[string][]byte{}
		root := path.Join("builtin", d.Name())
		fs.WalkDir(builtinFS, root, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || strings.HasSuffix(p, "_test.go") {
				return err
			}
			data, err := builtinFS.ReadFile(p)
			if err != nil {
				return err
			}
			files[strings.TrimPrefix(p, root+"/")] = data
			return nil
		})
		m, err := New(files)
		if err != nil {
			panic("built-in module " + d.Name() + ": " + err.Error())
		}
		if m.Kind == Go && goBuiltins[m.Name] == nil {
			panic("built-in module " + m.Name + " has no Go code")
		}
		m.Builtin = true
		builtins[m.Name] = m
	}
}

// Builtin is the built-in module called name (nil if there's none).
func Builtin(name string) *Module {
	builtinOnce.Do(loadBuiltins)
	return builtins[name]
}

// Builtins lists the built-in modules by name.
func Builtins() []*Module {
	builtinOnce.Do(loadBuiltins)
	out := make([]*Module, 0, len(builtins))
	for _, m := range builtins {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
