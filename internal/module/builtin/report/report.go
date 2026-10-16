// Package report is the built-in report module: it prints a round's
// artifacts (a report.md that mod claude wrote, a plan, a summary) into the
// step's log, where the operator and swim log read them. Each file gets a
// header with its size and SHA-256, the hash the round's END block records
// for an output. The step masks it like any output. It only shows a file:
// nothing in the round depends on what the file says.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gates-brightly/swimlane/internal/module/gomod"
)

const usage = "usage: mod report NAME... [--optional] [--max 256KiB]   (NAME: a file in $SWIM_OUT, or an absolute path)"

// Run is mod report NAME...
func Run(c *gomod.Ctx) error {
	a, err := gomod.ParseArgs(c.Args, []string{"max"}, []string{"optional"})
	if err != nil {
		return err
	}
	if len(a.Pos) == 0 || len(a.Rest) > 0 {
		return gomod.Usage{Msg: usage}
	}
	limit, err := size(a.Flag("max", "256KiB"))
	if err != nil {
		return err
	}
	optional := a.Flag("optional", "") == "true"
	out := c.Getenv("SWIM_OUT")
	var missing []string
	for i, name := range a.Pos {
		path := name
		if !filepath.IsAbs(name) {
			clean := filepath.ToSlash(filepath.Clean(name))
			if clean == ".." || strings.HasPrefix(clean, "../") {
				return gomod.Usage{Msg: "report: " + name + " isn't inside $SWIM_OUT (use an absolute path for another file)"}
			}
			if out == "" {
				return errors.New("report: SWIM_OUT is unset (it runs inside a lane's round)")
			}
			path = filepath.Join(out, filepath.FromSlash(clean))
		}
		if i > 0 {
			fmt.Fprintln(c.Stdout)
		}
		data, sum, total, err := read(path, limit)
		if err != nil {
			fmt.Fprintf(c.Stdout, "== %s: not there (%s)\n", name, short(err))
			missing = append(missing, name)
			continue
		}
		fmt.Fprintf(c.Stdout, "== %s  %s  sha256 %s\n", name, human(total), sum[:12])
		c.Stdout.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Fprintln(c.Stdout)
		}
		if int64(len(data)) < total {
			fmt.Fprintf(c.Stdout, "== (showed the first %s of %s: --max to show more)\n", human(int64(len(data))), human(total))
		}
	}
	if len(missing) > 0 && !optional {
		return fmt.Errorf("not there: %s (--optional to pass anyway)", strings.Join(missing, ", "))
	}
	return nil
}

// read returns a file's first limit bytes, the SHA-256 of all of it, and
// its size.
func read(path string, limit int64) ([]byte, string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, "", 0, err
	}
	if st.IsDir() {
		return nil, "", 0, errors.New("a directory")
	}
	h := sha256.New()
	head, err := io.ReadAll(io.LimitReader(io.TeeReader(f, h), limit))
	if err != nil {
		return nil, "", 0, err
	}
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, "", 0, err
	}
	return head, hex.EncodeToString(h.Sum(nil)), int64(len(head)) + n, nil
}

// size reads 256KiB, 1MiB, 4096.
func size(s string) (int64, error) {
	t := strings.TrimSpace(s)
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"KB", 1000}, {"MB", 1000 * 1000}, {"K", 1 << 10}, {"M", 1 << 20}} {
		if strings.HasSuffix(t, u.suf) {
			t, mult = strings.TrimSuffix(t, u.suf), u.m
			break
		}
	}
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n <= 0 {
		return 0, gomod.Usage{Msg: fmt.Sprintf("report: --max %q isn't a size like 256KiB", s)}
	}
	return n * mult, nil
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func short(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
