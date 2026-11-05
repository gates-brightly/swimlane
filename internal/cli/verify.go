package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// verifier gathers what swim verify finds: breaks (bad), warnings and
// notes, from the chain's entries.
type verifier struct {
	root string
	cfg  *config.Config

	bad, warn, notes []string
	unsigned         []int
	unsignedBad      string   // unsigned entries nothing vouches for (not BROKEN: nothing changed)
	covered          int      // unsigned entries a later signed one links to
	amended          []string // problems an amend entry acknowledged (swim verify --force)
	rounds           int
	approvals        int
}

// cmdVerify proves (or disproves) the chain of custody: every link follows
// the one before it, every signature matches the key recorded beside it,
// and every round's log, kept script, staged copy and inputs are what was
// recorded.
//
// It prints one answer, VERIFIED or NOT VERIFIED, and exits 1 for the
// second: when anything is BROKEN, or any entry is unsigned (nothing
// vouches for it, and a chain rewritten without a key looks the same)
// unless --allow-unsigned. --strict also fails on any warning.
func cmdVerify(args []string) error {
	var strict, asYAML, allowUnsigned, force bool
	rest, err := flags{bools: map[string]*bool{"strict": &strict, "yaml": &asYAML, "allow-unsigned": &allowUnsigned, "force": &force}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim verify [--strict] [--allow-unsigned] [--force] [--yaml]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	if !sign.Available() {
		return errors.New("swim verify needs ssh-keygen (OpenSSH 8.0 or later)")
	}
	v := &verifier{root: root, cfg: cfg}
	links, rerr := chain.Read(root)
	if rerr != nil {
		v.bad = append(v.bad, rerr.Error())
	}
	problems, unsigned := chain.Check(links)
	for _, pr := range problems {
		v.bad = append(v.bad, fmt.Sprintf("entry #%d (line %d): %s", pr.Seq, pr.Line, pr.What))
	}
	v.unsigned = unsigned
	// An unsigned entry with a signed one after it can't change unseen:
	// that signature covers it through the links (a chain that gained a
	// key later). The ones after the last signed entry nothing vouches for.
	covered, trailing := splitUnsigned(links, unsigned)
	v.covered = len(covered)
	if len(covered) > 0 {
		v.warn = append(v.warn, fmt.Sprintf("%d unsigned entries (#%s), recorded before a signed one that links to them, so they can't change unseen", len(covered), joinNums(covered)))
	}
	switch {
	case len(trailing) > 0 && allowUnsigned:
		v.warn = append(v.warn, fmt.Sprintf("%d unsigned entries at the end (linked, but no key vouches for them): #%s", len(trailing), joinNums(trailing)))
	case len(trailing) > 0:
		v.unsignedBad = fmt.Sprintf("%d unsigned entries at the end: #%s. Nothing vouches for them, and a chain rewritten with no key looks the same (set a signing key, ~/.ssh/id_ed25519 or signing_key; --allow-unsigned accepts them)", len(trailing), joinNums(trailing))
	}
	v.checkEntries(links)
	v.applyAmendments(links, problems)
	if force {
		if done, err := v.amend(); err != nil {
			return err
		} else if done {
			// Again, from the chain as it now stands.
			return cmdVerify(slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--force" }))
		}
	}
	failed := len(v.bad) > 0 || v.unsignedBad != ""
	if strict && (len(v.warn) > 0 || len(v.amended) > 0) {
		failed = true
	}
	if asYAML {
		if err := v.writeYAML(links, !failed); err != nil {
			return err
		}
	} else {
		v.print(links, !failed)
	}
	if failed {
		return exitError{1}
	}
	return nil
}

// verifyNote is what swim verify doesn't prove.
const verifyNote = "a chain replaced wholesale and re-signed with another key, or with entries removed from its end, is consistent too."

func (v *verifier) print(links []chain.Link, ok bool) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Printf("chain of custody: %s (%s, %s) in %s\n", plural(len(links), "entry"), plural(v.rounds, "round"), plural(v.approvals, "approval"), rel(v.root, chain.Path(v.root)))
	for _, n := range v.notes {
		fmt.Println("note: " + n)
	}
	for _, w := range v.warn {
		fmt.Println(p.Paint(ui.Yellow, "warning: "+w))
	}
	for _, b := range v.bad {
		fmt.Println(p.Paint(ui.Red, "BROKEN: "+b))
	}
	for _, a := range v.amended {
		fmt.Println(p.Paint(ui.Yellow, "AMENDED: "+a))
	}
	if v.unsignedBad != "" {
		fmt.Println(p.Paint(ui.Red, "UNSIGNED: "+v.unsignedBad))
	}
	// One answer, and what it can't vouch for.
	if !ok {
		why := v.unsignedBad
		if len(v.bad) > 0 {
			why = v.bad[0]
		}
		fmt.Println(p.Paint(ui.Red+ui.Bold, "NOT VERIFIED") + ": " + or(why, "see above"))
		return
	}
	signed := "every one signed and linked"
	switch trailing := len(v.unsigned) - v.covered; {
	case trailing > 0:
		signed = fmt.Sprintf("linked, %d unsigned (%d accepted: --allow-unsigned)", len(v.unsigned), trailing)
	case v.covered > 0:
		signed = fmt.Sprintf("linked, %d unsigned (covered by a later signed entry)", v.covered)
	}
	fmt.Printf("%s: %s, %s; logs and scripts as recorded\n", p.Paint(ui.Green+ui.Bold, "VERIFIED"), plural(len(links), "entry"), signed)
}

// verifyDoc is swim verify --yaml (schema swim.verify/v1).
type verifyDoc struct {
	Schema     string   `yaml:"schema"`
	Chain      string   `yaml:"chain"`
	Entries    int      `yaml:"entries"`
	Rounds     int      `yaml:"rounds"`
	Approvals  int      `yaml:"approvals"`
	OK         bool     `yaml:"ok"`         // what the exit code says
	Consistent string   `yaml:"consistent"` // yes: nothing BROKEN
	Note       string   `yaml:"note"`
	Broken     []string `yaml:"broken"`
	Unsigned   string   `yaml:"unsigned,omitempty"` // unsigned entries nothing vouches for
	Amended    []string `yaml:"amended"`            // gaps an amend entry acknowledged
	Warnings   []string `yaml:"warnings"`
	Notes      []string `yaml:"notes"`
}

func (v *verifier) writeYAML(links []chain.Link, ok bool) error {
	d := verifyDoc{Schema: "swim.verify/v1", Chain: rel(v.root, chain.Path(v.root)), Entries: len(links), Rounds: v.rounds, Approvals: v.approvals,
		OK: ok, Consistent: "yes", Note: verifyNote,
		Broken: nonNil(v.bad), Unsigned: v.unsignedBad, Amended: nonNil(v.amended), Warnings: nonNil(v.warn), Notes: nonNil(v.notes)}
	if len(v.bad) > 0 {
		d.Consistent = "no"
	}
	enc := yaml.NewEncoder(os.Stdout)
	enc.SetIndent(2)
	return enc.Encode(d)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// pinDate is the day of an RFC 3339 time.
func pinDate(at string) string {
	if len(at) >= 10 {
		return at[:10]
	}
	return or(at, "earlier")
}

// splitUnsigned divides the unsigned entries into those a later signed
// entry links to (covered) and those after the last signed one (trailing).
func splitUnsigned(links []chain.Link, unsigned []int) (covered, trailing []int) {
	lastSigned := -1
	for _, l := range links {
		if l.Sig != "" && l.Seq > lastSigned {
			lastSigned = l.Seq
		}
	}
	for _, n := range unsigned {
		if n < lastSigned {
			covered = append(covered, n)
		} else {
			trailing = append(trailing, n)
		}
	}
	return covered, trailing
}

// amendable says a problem swim verify --force may acknowledge: a gap in
// the chain (an entry removed, inserted or out of order, a link that
// doesn't follow) or something deleted (a round's log, a kept script).
// Changed content (a signature, a log, a script or staged copy that no
// longer matches) never is.
func amendable(problem string) bool {
	for _, s := range []string{
		"is missing before it (removed)", "are missing before it (removed)",
		"an entry was inserted, reordered or repeated", "prev hash doesn't match the entry before it",
		"(deleted, not archived)", ".sh is missing",
	} {
		if strings.Contains(problem, s) {
			return true
		}
	}
	return false
}

// applyAmendments moves the problems a signed amend entry acknowledged
// out of BROKEN and the warnings into AMENDED, each saying who signed for
// it. An amend entry whose own signature fails counts for nothing.
func (v *verifier) applyAmendments(links []chain.Link, problems []chain.Problem) {
	badSig := map[int]bool{}
	for _, p := range problems {
		if strings.HasPrefix(p.What, "signature doesn't match") {
			badSig[p.Seq] = true
		}
	}
	by := map[string]string{}
	for _, l := range links {
		if l.Type != "amend" || l.Sig == "" || badSig[l.Seq] {
			continue
		}
		for _, a := range l.Amended {
			if amendable(a) {
				by[a] = fmt.Sprintf("acknowledged by %s in entry #%d on %s; not the original signer", who(l.Signer), l.Seq, pinDate(l.At))
			}
		}
	}
	keep := func(list []string) []string {
		var out []string
		for _, p := range list {
			if note, ok := by[p]; ok {
				v.amended = append(v.amended, p+" ("+note+")")
			} else {
				out = append(out, p)
			}
		}
		return out
	}
	v.bad, v.warn = keep(v.bad), keep(v.warn)
}

// amend records the gaps and deletions not yet acknowledged in a signed
// amend entry (swim verify --force); done says it recorded one.
func (v *verifier) amend() (done bool, err error) {
	var pending []string
	for _, p := range append(slices.Clone(v.bad), v.warn...) {
		if amendable(p) {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		fmt.Println("swim verify --force: no gaps or deleted items to amend")
		return false, nil
	}
	key, err := runnerKey(v.cfg)
	if err != nil {
		return false, err
	}
	if key == nil {
		return false, errors.New("--force signs an amend entry, and there's no signing key (~/.ssh/id_ed25519 or signing_key)")
	}
	if _, err := chain.Append(v.root, chain.Entry{Type: "amend", Action: "swim verify --force", Amended: pending}, key); err != nil {
		return false, fmt.Errorf("recording the amendment: %v", err)
	}
	fmt.Printf("swim verify --force: amended %s, signed by %s (not the original signers):\n", plural(len(pending), "gap"), who(key.Public))
	for _, p := range pending {
		fmt.Println("  " + p)
	}
	fmt.Println()
	return true, nil
}
