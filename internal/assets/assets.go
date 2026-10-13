// Package assets embeds the files swim writes or prints: the lane script
// library, the lane script and stub templates, the gitignore block and help.
package assets

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"
	"time"
)

//go:embed lib.sh
var Lib string

//go:embed gitignore.txt
var Gitignore string

// GitShim is installed as .swim/bin/git for lanes (see blocked commands).
//
//go:embed git-shim.sh
var GitShim string

//go:embed lane.tmpl.sh
var laneTmpl string

//go:embed stub.tmpl.sh
var stubTmpl string

//go:embed lane.tmpl.py
var pyTmpl string

//go:embed lane.tmpl.js
var jsTmpl string

// Langs are the languages swim new writes a lane script in (--lang).
var Langs = []string{"sh", "py", "js", "ts"}

// LibFor returns the lane script library with SWIM_BIN pointing at bin unless
// the environment already set it.
func LibFor(bin string) string {
	return "SWIM_BIN=${SWIM_BIN:-" + shellQuote(bin) + "}\n" + Lib
}

// Lane script renders a fresh lane.N.sh.
func Script(lane int, goal, toolchain, job, owner string) (string, error) {
	return render(laneTmpl, map[string]any{
		"Job":       job,
		"Owner":     owner,
		"Lane":      lane,
		"Goal":      oneLine(goal),
		"Toolchain": strings.TrimSpace(toolchain),
		"Date":      time.Now().Format("2006-01-02"),
	})
}

// ScriptIn renders a fresh lane.N.<lang> (sh, py, js or ts).
func ScriptIn(lang string, lane int, goal, toolchain, job, owner string) (string, error) {
	data := map[string]any{
		"Job":   job,
		"Owner": owner,
		"Lane":  lane,
		"Goal":  oneLine(goal),
		"Date":  time.Now().Format("2006-01-02"),
	}
	switch lang {
	case "", "sh":
		return Script(lane, goal, toolchain, job, owner)
	case "py":
		return render(pyTmpl, data)
	case "js":
		data["Import"] = `const { spawnSync } = require("node:child_process");`
		data["ArgsType"], data["RetType"] = "", ""
		return render(jsTmpl, data)
	case "ts":
		// No #! line: TypeScript runners differ (tsx, ts-node, bun, deno), so
		// swim asks the operator once and remembers (runners.ts).
		data["Import"] = `import { spawnSync } from "node:child_process";`
		data["ArgsType"], data["RetType"] = ": string[]", ": number"
		out, err := render(jsTmpl, data)
		return strings.TrimPrefix(out, "#!/usr/bin/env node\n"), err
	}
	return "", fmt.Errorf("unknown language %q (use one of %s)", lang, strings.Join(Langs, ", "))
}

// Stub renders a "nothing pending" placeholder.
func Stub(lane int, message string) (string, error) {
	return render(stubTmpl, map[string]any{"Lane": lane, "Message": shellSafe(oneLine(message))})
}

func render(t string, data any) (string, error) {
	tmpl, err := template.New("t").Parse(t)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// shellSafe keeps a message usable inside the stub's double-quoted echo.
func shellSafe(s string) string {
	return strings.NewReplacer(`"`, "'", "`", "'", "$", "", `\`, "/").Replace(s)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
