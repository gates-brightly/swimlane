package cli

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/store"
	"github.com/gates-brightly/swimlane/internal/ui"
)

const storeUsage = "usage: swim store [ls] [--all] | show RUN [N] [--log | --script | --file PATH] | verify | path"

// swim store [ls] [--all] | show RUN [N] [--log|--script|--file PATH] | verify | path
func cmdStore(args []string) error {
	sub := "ls"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	_, cfg, err := repo()
	if err != nil {
		return err
	}
	st, slug, err := store.Open(cfg)
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("the store is off (store_dir: %s in %s)", config.StoreOff, cfg.Path)
	}
	switch sub {
	case "ls", "list":
		return storeList(st, slug, args)
	case "show":
		return storeShow(st, slug, args)
	case "verify":
		if len(args) > 0 {
			return usagef("usage: swim store verify")
		}
		return storeVerify(st, slug, cfg)
	case "path":
		if len(args) > 0 {
			return usagef("usage: swim store path")
		}
		fmt.Println(st.Dir)
		return nil
	}
	return usagef(storeUsage)
}

// storeListMax is how many rounds swim store ls shows without --all.
const storeListMax = 20

func storeList(st *store.Store, slug string, args []string) error {
	var all bool
	if rest, err := (flags{bools: map[string]*bool{"all": &all}}).parse(args); err != nil {
		return err
	} else if len(rest) > 0 {
		return usagef("usage: swim store ls [--all]")
	}
	rounds, err := st.Rounds(slug)
	if len(rounds) == 0 {
		fmt.Printf("nothing kept for this repo yet (%s)\n", st.Dir)
		return err
	}
	p := painter()
	shown := rounds
	if !all && len(shown) > storeListMax {
		shown = shown[len(shown)-storeListMax:]
	}
	for i := len(shown) - 1; i >= 0; i-- {
		r := shown[i]
		word, colour := "DIDN'T FINISH", ui.Yellow
		if r.Result != "" {
			word = strings.ToUpper(r.Result)
			colour = ui.StateColor(map[string]string{"passed": "PASS", "failed": "FAIL", "skipped": "SKIP", "interrupted": "INTERRUPTED"}[r.Result])
		}
		what := r.Round
		if r.Job != "" {
			what = strings.TrimSpace(r.Job + "  " + what)
		}
		fmt.Printf("%s  %s  lane %-2d %s  %s\n", localTime(r.StoredAt), r.Run, r.Lane, p.Paint(colour, fmt.Sprintf("%-13s", word)), what)
	}
	if hidden := len(rounds) - len(shown); hidden > 0 {
		fmt.Println(p.Paint(ui.Dim, fmt.Sprintf("... %d older (--all)", hidden)))
	}
	fmt.Println(p.Paint(ui.Dim, "swim store show RUN [N]: what one ran and wrote"))
	return err
}

func localTime(rfc string) string {
	t, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		return rfc
	}
	return t.Local().Format("2006-01-02 15:04")
}

// findRound is the record of RUN (a prefix of its run id is enough) and
// lane n (0: the run's only lane).
func findRound(st *store.Store, slug, run string, n int) (*store.Round, error) {
	rounds, err := st.Rounds(slug)
	var match []*store.Round
	for _, r := range rounds {
		if strings.HasPrefix(r.Run, run) && (n == 0 || r.Lane == n) {
			match = append(match, r)
		}
	}
	switch {
	case len(match) == 1:
		return match[0], nil
	case len(match) == 0 && err != nil:
		return nil, err
	case len(match) == 0:
		return nil, fmt.Errorf("no round of run %s kept for this repo (swim store ls)", run)
	}
	var which []string
	for _, r := range match {
		which = append(which, fmt.Sprintf("%s %d", r.Run, r.Lane))
	}
	return nil, usagef("%s names %d rounds; say which: swim store show %s", run, len(match), strings.Join(which, " | "))
}

func storeShow(st *store.Store, slug string, args []string) error {
	var showLog, showScript bool
	var file string
	rest, err := (flags{bools: map[string]*bool{"log": &showLog, "script": &showScript}, strs: map[string]*string{"file": &file}}).parse(args)
	if err != nil {
		return err
	}
	picked := 0
	for _, b := range []bool{showLog, showScript, file != ""} {
		if b {
			picked++
		}
	}
	if len(rest) < 1 || len(rest) > 2 || picked > 1 {
		return usagef("usage: swim store show RUN [N] [--log | --script | --file PATH]")
	}
	n := 0
	if len(rest) == 2 {
		if n, err = strconv.Atoi(rest[1]); err != nil || n < 1 {
			return usagef("swim store show: %q isn't a lane number", rest[1])
		}
	}
	r, err := findRound(st, slug, rest[0], n)
	if err != nil {
		return err
	}
	print := func(what, sum string) error {
		if sum == "" {
			return fmt.Errorf("%s lane %d has no %s kept", r.Run, r.Lane, what)
		}
		data, err := st.Get(sum)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	}
	switch {
	case showLog:
		if r.Log == "" {
			return fmt.Errorf("%s lane %d didn't finish: no log kept (what it ran is: swim store show %s %d --script)", r.Run, r.Lane, r.Run, r.Lane)
		}
		return print("log", r.Log)
	case showScript:
		return print("script", r.Script)
	case file != "":
		for _, f := range r.Files {
			if f.Path == file {
				return print(file, f.SHA256)
			}
		}
		var paths []string
		for _, f := range r.Files {
			paths = append(paths, f.Path)
		}
		return fmt.Errorf("%s lane %d ran no %s; its files: %s", r.Run, r.Lane, file, strings.Join(paths, ", "))
	}
	p := painter()
	result := "didn't finish (no summary: killed, or still running)"
	if r.Result != "" {
		result = r.Result
		if r.Exit != nil {
			result += fmt.Sprintf(", exit %d", *r.Exit)
		}
	}
	fmt.Printf("run %s  lane %d\n", r.Run, r.Lane)
	for _, kv := range [][2]string{{"job", r.Job}, {"round", r.Round}, {"repo", r.Repo}, {"stored", localTime(r.StoredAt)}, {"result", result}} {
		if kv[1] != "" {
			fmt.Printf("  %-9s %s\n", kv[0]+":", kv[1])
		}
	}
	if !r.Staged {
		fmt.Println("  " + p.Paint(ui.Dim, "run directly with bash: its files were read from the working tree as the round ended"))
	}
	fmt.Println("  files:")
	for _, f := range r.Files {
		fmt.Printf("    %s  %s\n", short12(f.SHA256), f.Path)
	}
	fmt.Printf("  %-9s %s\n", "script:", short12(r.Script)+p.Paint(ui.Dim, "  (with its includes, as the chain hashes it: --script)"))
	if r.Log != "" {
		fmt.Printf("  %-9s %s\n", "log:", short12(r.Log)+p.Paint(ui.Dim, "  (the round's section: --log)"))
	}
	if problems := st.Check(r); len(problems) > 0 {
		for _, pr := range problems {
			fmt.Println("  " + p.Paint(ui.Red, "BROKEN  "+pr.What))
		}
		return exitError{1}
	}
	return nil
}

// storeVerify checks what's kept against itself and the chain: every
// object a record names is there with the bytes its hash names, and every
// round the chain recorded since the store's first record is kept, with
// the script and log hashes its chain entry holds.
func storeVerify(st *store.Store, slug string, cfg *config.Config) error {
	rounds, err := st.Rounds(slug)
	var problems []string
	if err != nil {
		problems = append(problems, err.Error())
	}
	kept := map[string]*store.Round{}
	first := ""
	objects := 0
	for _, r := range rounds {
		kept[r.Run+" "+strconv.Itoa(r.Lane)] = r
		if first == "" || r.StoredAt < first {
			first = r.StoredAt
		}
		objects += len(r.Objects())
		for _, pr := range st.Check(r) {
			problems = append(problems, pr.String())
		}
	}
	links, err := chain.Read(cfg.Root)
	if err != nil {
		problems = append(problems, "the chain: "+err.Error())
	}
	inChain, before := 0, 0
	for _, l := range links {
		if l.Type != "round" {
			continue
		}
		inChain++
		r := kept[l.Run+" "+strconv.Itoa(l.Lane)]
		switch {
		case r == nil && (first == "" || l.At < first):
			before++ // recorded before this repo's store began
		case r == nil:
			problems = append(problems, fmt.Sprintf("%s lane %d: the chain recorded the round (entry #%d), but it isn't kept", l.Run, l.Lane, l.Seq))
		default:
			if l.Script != "" && l.Script != r.Script {
				problems = append(problems, fmt.Sprintf("%s lane %d: chain entry #%d ran script %s, the store kept %s", l.Run, l.Lane, l.Seq, short12(l.Script), short12(r.Script)))
			}
			if l.Log != "" && l.Log != r.Log {
				problems = append(problems, fmt.Sprintf("%s lane %d: chain entry #%d hashed its log as %s, the store kept %s", l.Run, l.Lane, l.Seq, short12(l.Log), short12(r.Log)))
			}
		}
	}
	sort.Strings(problems)
	p := painter()
	for _, pr := range problems {
		fmt.Println(p.Paint(ui.Red, "BROKEN  ") + pr)
	}
	summary := fmt.Sprintf("%s kept (%s) in %s; %s in the chain", plural(len(rounds), "round"), plural(objects, "object"), st.Dir, plural(inChain, "round"))
	if before > 0 {
		summary += fmt.Sprintf(", %d from before the store began", before)
	}
	if len(problems) > 0 {
		fmt.Println(p.Paint(ui.Red+ui.Bold, "NOT VERIFIED: ") + summary + ": " + plural(len(problems), "problem"))
		return exitError{1}
	}
	fmt.Println(p.Paint(ui.Green+ui.Bold, "VERIFIED: ") + summary)
	return nil
}
