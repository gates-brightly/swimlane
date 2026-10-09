// Package assets embeds the files swim writes or prints: the lane script
// library, the lane script and stub templates, the gitignore block and help.
package assets

import (
	"bytes"
	_ "embed"
	"strings"
	"text/template"
	"time"
)

//go:embed lib.sh
var Lib string

//go:embed gitignore.txt
var Gitignore string

//go:embed lane.tmpl.sh
var laneTmpl string

//go:embed stub.tmpl.sh
var stubTmpl string

// LibFor returns the lane script library with SWIM_BIN pointing at bin unless
// the environment already set it.
func LibFor(bin string) string {
	return "SWIM_BIN=${SWIM_BIN:-" + shellQuote(bin) + "}\n" + Lib
}

// Lane script renders a fresh lane.N.sh.
func Script(lane int, goal, toolchain, job string) (string, error) {
	return render(laneTmpl, map[string]any{
		"Job":       job,
		"Lane":      lane,
		"Goal":      oneLine(goal),
		"Toolchain": strings.TrimSpace(toolchain),
		"Date":      time.Now().Format("2006-01-02"),
	})
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
