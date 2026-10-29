package step

import (
	"os"
	"path/filepath"
	"strings"
)

// stagedMisses are the paths a failed step's command names that its
// staged copy (.swim/runs/<run>/<N>/, the working directory of a staged
// lane) doesn't have but the repo at root does: the likely cause of a
// bare "No such file or directory", since only the script and its
// # Includes: are staged. Nil when the step doesn't run in a staged copy.
func stagedMisses(root string, args []string) []string {
	cwd, err := os.Getwd()
	if err != nil || root == "" {
		return nil
	}
	runs := filepath.Join(root, ".swim", "runs") + string(os.PathSeparator)
	if !strings.HasPrefix(cwd+string(os.PathSeparator), runs) {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range args {
		// Words of the argument, so `bash -c 'cat deploy.yaml | ...'`
		// and --config=deploy.yaml count too.
		for _, w := range strings.FieldsFunc(a, func(r rune) bool {
			return strings.ContainsRune(" \t\n'\"`;|&<>()=", r)
		}) {
			if seen[w] || filepath.IsAbs(w) || strings.HasPrefix(w, "-") || strings.ContainsAny(w, "$*?[~") {
				continue
			}
			seen[w] = true
			rel := filepath.Clean(w)
			if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			if _, err := os.Stat(filepath.Join(cwd, rel)); err == nil {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
				out = append(out, rel)
			}
		}
	}
	return out
}
