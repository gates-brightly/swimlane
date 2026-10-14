package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
	"github.com/gates-brightly/swimlane/internal/lookup"
	"github.com/gates-brightly/swimlane/internal/manifest"
	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/trust"
	"github.com/gates-brightly/swimlane/internal/ui"
)

// Level results. Each level of swim verify says how far the chain can be
// believed, and its line says what it doesn't prove.
const (
	levelYes     = "yes"
	levelNo      = "no"
	levelPartial = "partial" // held for every signed entry; some are unsigned
	levelNA      = "n/a"     // nothing to check it against
)

// level is one line of swim verify.
type level struct {
	Name   string
	Result string
	Detail string
}

// verifier gathers what swim verify finds: breaks (bad), warnings and
// notes, from the chain entries, the approvals files and each level.
type verifier struct {
	root string
	cfg  *config.Config
	lk   *lookup.Lookup
	mans map[int]*manifest.Manifest

	bad, warn, notes []string
	files            []string        // one line per approvals file
	unlisted         map[string]bool // fingerprints of signers not in lookup.yml
	unsigned         []int
	rounds           int
	approvals        int
}

// cmdVerify proves (or disproves) the chain of custody, in levels:
//
//	consistent  every link follows the one before it, every signature
//	            matches the key recorded beside it, and every round's log,
//	            kept script, staged copy and inputs, and every signed
//	            approval, are what was recorded
//	trusted     every signer is a key in trust.yml, pinned outside the repo
//
// Exits 1 when a level fails; --strict also when one can't be checked
// (n/a), on unsigned entries and on any warning.
func cmdVerify(args []string) error {
	var strict, asYAML bool
	var pinFlag string
	rest, err := flags{bools: map[string]*bool{"strict": &strict, "yaml": &asYAML},
		strs: map[string]*string{"trust-pin": &pinFlag}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim verify [--strict] [--trust-pin SHA256] [--yaml]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	lk, err := lookup.Load(root)
	if err != nil {
		return err
	}
	if !sign.Available() {
		return errors.New("swim verify needs ssh-keygen (OpenSSH 8.0 or later)")
	}
	v := &verifier{root: root, cfg: cfg, lk: lk, mans: map[int]*manifest.Manifest{}, unlisted: map[string]bool{}}
	links, rerr := chain.Read(root)
	if rerr != nil {
		v.bad = append(v.bad, rerr.Error())
	}
	problems, unsigned := chain.Check(links)
	for _, pr := range problems {
		v.bad = append(v.bad, fmt.Sprintf("entry #%d (line %d): %s", pr.Seq, pr.Line, pr.What))
	}
	v.unsigned = unsigned
	if len(unsigned) > 0 {
		v.warn = append(v.warn, fmt.Sprintf("%d unsigned entries (linked, but no key vouches for them): #%s", len(unsigned), joinNums(unsigned)))
	}
	v.checkEntries(links)
	v.checkApprovalFiles()
	levels := []level{
		v.consistent(links),
		v.trusted(links, or(pinFlag, os.Getenv(trust.PinEnv))),
	}

	failed := len(v.bad) > 0
	for _, l := range levels {
		switch {
		case l.Result == levelNo:
			failed = true
		case strict && (l.Result == levelNA || l.Result == levelPartial):
			failed = true
		}
	}
	if strict && len(v.warn) > 0 {
		failed = true
	}
	if asYAML {
		if err := v.writeYAML(links, levels, !failed); err != nil {
			return err
		}
	} else {
		v.print(links, levels)
	}
	if failed {
		return exitError{1}
	}
	return nil
}

// consistent is the chain checked against itself and what's on disk.
func (v *verifier) consistent(links []chain.Link) level {
	if len(v.bad) > 0 {
		return level{"consistent", levelNo, fmt.Sprintf("%d problems (BROKEN, above)", len(v.bad))}
	}
	return level{"consistent", levelYes, fmt.Sprintf("%d entries, links and signatures match; logs, scripts and approvals as recorded", len(links))}
}

// trusted checks every signer against trust.yml's keys, pinned (on first
// use, by swim keys pin, or by pin: --trust-pin, SWIM_TRUST_PIN) outside
// the repo. Without trust.yml it falls back to warning about signers
// lookup.yml doesn't name.
func (v *verifier) trusted(links []chain.Link, pin string) level {
	const name = "trusted"
	tf, err := trust.Load(v.root)
	if err != nil {
		return level{name, levelNo, err.Error()}
	}
	if tf == nil {
		var fps []string
		for fp := range v.unlisted {
			fps = append(fps, fp)
		}
		sort.Strings(fps)
		for _, fp := range fps {
			v.warn = append(v.warn, "entries signed by a key not in lookup.yml: "+fp)
		}
		return level{name, levelNA, "no trust.yml: the signers' keys are only those recorded in the chain (swim keys trust)"}
	}
	var p *trust.Pin
	explicit := pin != ""
	if explicit {
		p = &trust.Pin{SHA: strings.TrimSpace(pin), From: "--trust-pin"}
	} else if p, err = trust.LoadPin(v.root); err != nil {
		return level{name, levelNo, err.Error()}
	}
	res := trust.Check(v.root, tf, p)
	pinned := ""
	switch res.Status {
	case trust.Unsigned, trust.Malformed:
		return level{name, levelNo, res.Why + " (swim keys pin accepts it as it is now)"}
	case trust.TOFU:
		pinned = "pinned now, on first use"
		if _, err := trust.SavePin(v.root, tf.Keys, nil); err != nil {
			v.warn = append(v.warn, "trust.yml: couldn't pin it: "+err.Error())
		}
	case trust.Advanced:
		pinned = fmt.Sprintf("%d signed changes since the pin", res.Steps)
		if !explicit {
			if _, err := trust.SavePin(v.root, tf.Keys, res.KeyList()); err != nil {
				v.warn = append(v.warn, "trust.yml: couldn't move the pin: "+err.Error())
			}
		}
	case trust.Match:
		pinned = "pinned " + pinDate(p.At)
		if explicit {
			pinned = "matches " + p.From
		}
	}
	if explicit && res.Status != trust.Match {
		pinned += " from " + p.From
	}
	signers := map[string]bool{}
	for _, l := range links {
		if l.Sig != "" {
			signers[sign.KeyOnly(l.Signer)] = true
		}
		for _, ap := range l.Approvers {
			if ap.Sig != "" {
				signers[sign.KeyOnly(ap.Key)] = true
			}
		}
	}
	if bad := chain.Trust(links, res.Trusted); len(bad) > 0 {
		// One line per key, in the order they first sign.
		var keys []string
		seqs := map[string][]int{}
		for _, u := range bad {
			k := sign.KeyOnly(u.Key)
			if seqs[k] == nil {
				keys = append(keys, k)
			}
			seqs[k] = append(seqs[k], u.Seq)
		}
		for _, k := range keys {
			v.bad = append(v.bad, fmt.Sprintf("%d signatures by %s, a key not in trust.yml: entries #%s", len(seqs[k]), sign.Fingerprint(k), joinNums(slices.Compact(seqs[k]))))
		}
		return level{name, levelNo, fmt.Sprintf("%d signatures by %d keys not in trust.yml (%s)", len(bad), len(keys), pinned)}
	}
	if len(v.unsigned) > 0 {
		return level{name, levelPartial, fmt.Sprintf("%d unsigned entries; the rest signed by %d keys in trust.yml (%s)", len(v.unsigned), len(signers), pinned)}
	}
	return level{name, levelYes, fmt.Sprintf("%d signers, all in trust.yml (%s)", len(signers), pinned)}
}

// note is what the levels together don't prove.
func verifyNote(levels []level) string {
	for _, l := range levels {
		if l.Name == "trusted" && l.Result != levelYes {
			return "a chain re-signed wholesale with another key is only caught by trusted (trust.yml, pinned outside the repo)."
		}
	}
	return "the levels prove who signed this history; they don't prove the scripts were safe, nor catch entries removed from its end."
}

func (v *verifier) print(links []chain.Link, levels []level) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Printf("chain of custody: %d entries (%d rounds, %d approvals) in %s\n", len(links), v.rounds, v.approvals, rel(v.root, chain.Path(v.root)))
	for _, f := range v.files {
		fmt.Println(f)
	}
	for _, n := range v.notes {
		fmt.Println("note: " + n)
	}
	for _, w := range v.warn {
		fmt.Println(p.Paint(ui.Yellow, "warning: "+w))
	}
	for _, b := range v.bad {
		fmt.Println(p.Paint(ui.Red, "BROKEN: "+b))
	}
	for _, l := range levels {
		c := ui.Yellow
		switch l.Result {
		case levelYes:
			c = ui.Green
		case levelNo:
			c = ui.Red
		}
		fmt.Printf("%-10s  %s  %s\n", l.Name, p.Paint(c, fmt.Sprintf("%-3s", l.Result)), l.Detail)
	}
	fmt.Println("note: " + verifyNote(levels))
}

// verifyDoc is swim verify --yaml (schema swim.verify/v1): each level a
// field of its own, so CI can require any combination.
type verifyDoc struct {
	Schema     string            `yaml:"schema"`
	Chain      string            `yaml:"chain"`
	Entries    int               `yaml:"entries"`
	Rounds     int               `yaml:"rounds"`
	Approvals  int               `yaml:"approvals"`
	OK         bool              `yaml:"ok"` // what the exit code says
	Consistent string            `yaml:"consistent"`
	Trusted    string            `yaml:"trusted"`
	Details    map[string]string `yaml:"details"` // level -> its line
	Note       string            `yaml:"note"`
	Broken     []string          `yaml:"broken"`
	Warnings   []string          `yaml:"warnings"`
	Notes      []string          `yaml:"notes"`
	Files      []string          `yaml:"approval_files"`
}

func (v *verifier) writeYAML(links []chain.Link, levels []level, ok bool) error {
	d := verifyDoc{Schema: "swim.verify/v1", Chain: rel(v.root, chain.Path(v.root)), Entries: len(links), Rounds: v.rounds, Approvals: v.approvals,
		OK: ok, Details: map[string]string{}, Note: verifyNote(levels),
		Broken: nonNil(v.bad), Warnings: nonNil(v.warn), Notes: nonNil(v.notes), Files: nonNil(v.files)}
	for _, l := range levels {
		d.Details[l.Name] = l.Detail
		switch l.Name {
		case "consistent":
			d.Consistent = l.Result
		case "trusted":
			d.Trusted = l.Result
		}
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

// pinDate is the day a pin was made.
func pinDate(at string) string {
	if len(at) >= 10 {
		return at[:10]
	}
	return or(at, "earlier")
}
