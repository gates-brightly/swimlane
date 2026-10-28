package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/gates-brightly/swimlane/internal/anchor"
	"github.com/gates-brightly/swimlane/internal/approval"
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
	unsignedBad      string   // unsigned entries nothing vouches for (not BROKEN: nothing changed)
	covered          int      // unsigned entries a later signed one links to
	amended          []string // problems an amend entry acknowledged (swim verify --force)
	rounds           int
	approvals        int
	// trust says whether a key is trusted (nil without trust.yml): for
	// checkpoint signers too.
	trust func(pub string) bool
	// anchoredAt is when the checkpoint anchored proves the chain against
	// was taken.
	anchoredAt string
}

// cmdVerify proves (or disproves) the chain of custody, in levels:
//
//	consistent  every link follows the one before it, every signature
//	            matches the key recorded beside it, and every round's log,
//	            kept script, staged copy and inputs, and every signed
//	            approval, are what was recorded
//	trusted     every signer is a key in trust.yml, pinned outside the repo
//	anchored    the chain still holds the head of the newest checkpoint at
//	            each anchor (chain_anchors, or --against DEST), and the
//	            entries exported there
//	authorized  every approval recorded met lookup.yml's rule for its guard
//	            (signed ones: by enough of the right people)
//
// It prints one answer, VERIFIED or NOT VERIFIED, and exits 1 for the
// second: when a level fails, or any entry is unsigned (nothing vouches
// for it, and a chain rewritten without a key looks the same) unless
// --allow-unsigned. --strict also fails when a level can't be checked
// (n/a) and on any warning; --levels prints each level.
func cmdVerify(args []string) error {
	var strict, asYAML, allowUnsigned, showLevels, force bool
	var pinFlag, against string
	rest, err := flags{bools: map[string]*bool{"strict": &strict, "yaml": &asYAML, "allow-unsigned": &allowUnsigned, "levels": &showLevels, "force": &force},
		strs: map[string]*string{"trust-pin": &pinFlag, "against": &against}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim verify [--strict] [--allow-unsigned] [--levels] [--force] [--trust-pin SHA256] [--against DEST] [--yaml]")
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
	v.checkApprovalFiles()
	v.applyAmendments(links, problems)
	if force {
		if done, err := v.amend(); err != nil {
			return err
		} else if done {
			// Again, from the chain as it now stands.
			return cmdVerify(slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "--force" }))
		}
	}
	levels := []level{
		v.consistent(links),
		v.trusted(links, or(pinFlag, os.Getenv(trust.PinEnv))),
		v.anchored(links, against),
		v.authorized(links),
	}

	failed := len(v.bad) > 0 || v.unsignedBad != ""
	for _, l := range levels {
		switch {
		case l.Result == levelNo:
			failed = true
		case strict && (l.Result == levelNA || l.Result == levelPartial):
			failed = true
		}
	}
	if strict && (len(v.warn) > 0 || len(v.amended) > 0) {
		failed = true
	}
	if asYAML {
		if err := v.writeYAML(links, levels, !failed); err != nil {
			return err
		}
	} else {
		v.print(links, levels, showLevels, !failed)
	}
	if failed {
		return exitError{1}
	}
	return nil
}

// consistent is the chain checked against itself and what's on disk.
func (v *verifier) consistent(links []chain.Link) level {
	if len(v.bad) > 0 {
		return level{"consistent", levelNo, fmt.Sprintf("%s (BROKEN, above)", plural(len(v.bad), "problem"))}
	}
	return level{"consistent", levelYes, fmt.Sprintf("%s, links and signatures match; logs, scripts and approvals as recorded", plural(len(links), "entry"))}
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
	v.trust = res.Trusted
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
			signers[sign.KeyOnly(l.SignedBy())] = true
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

// anchored checks the chain against the newest checkpoint at each anchor
// (chain_anchors; --against DEST checks that one instead, and the entries
// exported there): it must still hold the checkpointed entry.
func (v *verifier) anchored(links []chain.Link, against string) level {
	const name = "anchored"
	specs := v.cfg.ChainAnchors
	if against != "" {
		specs = []string{against}
	}
	if len(specs) == 0 {
		return level{name, levelNA, "no anchors configured (chain_anchors): entries removed from the end, or a chain replaced wholesale, can't be caught"}
	}
	exported := 0
	if against != "" {
		a, err := openAnchor(v.root, against)
		if err != nil {
			return level{name, levelNo, err.Error()}
		}
		theirs, err := a.Entries()
		switch {
		case errors.Is(err, anchor.ErrNoEntries):
		case err != nil:
			return level{name, levelNo, fmt.Sprintf("%s: %v", against, err)}
		default:
			local, err := anchor.LocalLines(v.root)
			if err != nil {
				return level{name, levelNo, err.Error()}
			}
			if err := anchor.Diverged(theirs, local); err != nil {
				return level{name, levelNo, fmt.Sprintf("%s: %v", against, err)}
			}
			exported = len(theirs)
		}
	}
	var best *chain.Checkpoint
	from, reached := "", 0
	for _, spec := range specs {
		cps, err := v.checkpoints(spec)
		if err != nil {
			v.warn = append(v.warn, fmt.Sprintf("anchor %s: %v", spec, err))
			continue
		}
		reached++
		// Every checkpoint, not only the newest: a chain rewritten and
		// padded past the last one, then checkpointed again, still fails
		// every checkpoint taken before the rewrite. Newest first, so a
		// failure names the newest checkpoint the chain falls short of.
		sort.Slice(cps, func(i, j int) bool { return cps[i].Seq > cps[j].Seq })
		for i := range cps {
			cp := &cps[i]
			switch r := chain.CheckAgainst(links, *cp); r.Status {
			case chain.Truncated:
				return level{name, levelNo, fmt.Sprintf("chain truncated or replaced after checkpoint seq %d (%s, %s): it has %d entries", cp.Seq, cp.At, spec, len(links))}
			case chain.Replaced:
				return level{name, levelNo, fmt.Sprintf("chain replaced: entry #%d isn't the one checkpointed at seq %d (%s, %s)", cp.Seq, cp.Seq, cp.At, spec)}
			}
			if best == nil || cp.Seq > best.Seq {
				best, from = cp, spec
			}
		}
	}
	switch {
	case best != nil:
		v.anchoredAt = best.At
		return level{name, levelYes, fmt.Sprintf("extends checkpoint seq %d (%s) from %s; %d entries since", best.Seq, best.At, from, len(links)-best.Seq)}
	case exported > 0:
		return level{name, levelYes, fmt.Sprintf("extends the %d entries exported to %s; %d since", exported, against, len(links)-exported)}
	case reached == 0:
		return level{name, levelNA, "no anchor could be read (warnings above)"}
	}
	return level{name, levelNA, "no checkpoint at " + strings.Join(specs, ", ") + " yet (swim chain checkpoint)"}
}

// signedMethods are how an approval is recorded when signatures (approvals
// files, or answers signed through the hub) approved it.
var signedMethods = map[string]bool{"signature": true, "standing": true, "signature+standing": true, approval.HubSigned: true}

// authorized checks each approve entry against lookup.yml's rule for its
// guard: a signed approval's approvers (named by their keys) must meet
// it; a prompt, flag, -y or queue approval fails only a rule that says
// signed: true, and a hub button click any require: rule for its guard (it
// counts only where none names it). A failure under the lookup.yml the entry recorded
// (lookup_sha256) is an error; under one that has changed since, only a
// warning: the rules it met then aren't known.
func (v *verifier) authorized(links []chain.Link) level {
	const name = "authorized"
	if v.lk.Path == "" {
		return level{name, levelNA, "no lookup.yml: no rules to check approvals against"}
	}
	current := lookupSHA(v.root)
	total, failed, unsure := 0, 0, 0
	for _, l := range links {
		if l.Type != "approve" {
			continue
		}
		total++
		rule := v.lk.RuleFor(l.Guard)
		why := ""
		if signedMethods[l.Method] {
			var users []string
			for _, ap := range l.Approvers {
				if ap.Sig == "" {
					continue
				}
				if u, ok := v.lk.Who(ap.Key); ok {
					users = append(users, u)
				}
			}
			if ok, need := v.lk.Satisfied(rule, users); !ok {
				why = need
			}
		} else if rule.Signed {
			why = fmt.Sprintf("approved by %s, but lookup.yml says %s needs signed approvals", or(l.Method, "an unknown method"), l.Guard)
		} else if l.Method == approval.HubButton && v.lk.Explicit(l.Guard) {
			why = fmt.Sprintf("approved by a hub button click, but lookup.yml has a require: rule for %s (only signed answers count)", l.Guard)
		}
		if why == "" {
			continue
		}
		msg := fmt.Sprintf("entry #%d: %s (swim %d, run %s): %s", l.Seq, l.Guard, l.Lane, l.Run, why)
		if l.Lookup != "" && l.Lookup == current {
			failed++
			v.bad = append(v.bad, msg)
		} else {
			unsure++
			v.warn = append(v.warn, msg+" (under today's lookup.yml; it was approved under another)")
		}
	}
	switch {
	case total == 0:
		return level{name, levelNA, "no approvals recorded"}
	case failed > 0:
		return level{name, levelNo, fmt.Sprintf("%d of %d approvals didn't meet their lookup.yml rule", failed, total)}
	case unsure > 0:
		return level{name, levelPartial, fmt.Sprintf("%d approvals; %d don't meet today's lookup.yml, which changed since they were recorded", total, unsure)}
	}
	return level{name, levelYes, fmt.Sprintf("%s, each met its lookup.yml rule", plural(total, "approval"))}
}

// note is what the levels together don't prove.
func (v *verifier) note(levels []level) string {
	res := map[string]string{}
	for _, l := range levels {
		res[l.Name] = l.Result
	}
	switch {
	case res["anchored"] == levelYes && v.anchoredAt != "":
		return "anchoring proves this history existed by " + v.anchoredAt + "; it doesn't prove the scripts were safe, nor anything about entries after it."
	case res["anchored"] == levelYes:
		return "anchoring proves this history was exported; it doesn't prove the scripts were safe, nor anything about entries after it."
	case res["trusted"] != levelYes:
		return "a chain re-signed wholesale with another key is only caught by trusted (trust.yml, pinned outside the repo)."
	}
	return "without an anchor, entries removed from the end of the chain can't be caught (chain_anchors)."
}

func (v *verifier) print(links []chain.Link, levels []level, showLevels, ok bool) {
	p := ui.Painter{On: ui.ColorEnabled(os.Stdout)}
	fmt.Printf("chain of custody: %s (%s, %s) in %s\n", plural(len(links), "entry"), plural(v.rounds, "round"), plural(v.approvals, "approval"), rel(v.root, chain.Path(v.root)))
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
	for _, a := range v.amended {
		fmt.Println(p.Paint(ui.Yellow, "AMENDED: "+a))
	}
	if v.unsignedBad != "" {
		fmt.Println(p.Paint(ui.Red, "UNSIGNED: "+v.unsignedBad))
	}
	if showLevels {
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
		fmt.Println("note: " + v.note(levels))
	}
	// One answer, and what it can't vouch for.
	if !ok {
		why := ""
		for _, l := range levels {
			if l.Result == levelNo {
				why = l.Name + ": " + l.Detail
				break
			}
		}
		if why == "" && len(v.bad) > 0 {
			why = v.bad[0]
		}
		if why == "" {
			why = v.unsignedBad
		}
		fmt.Println(p.Paint(ui.Red+ui.Bold, "NOT VERIFIED") + ": " + or(why, "see above"))
		return
	}
	var caveats []string
	for _, l := range levels {
		if l.Result == levelNA || l.Result == levelPartial {
			caveats = append(caveats, l.Name+" "+string(l.Result)+": "+l.Detail)
		}
	}
	signed := "every one signed and linked"
	switch trailing := len(v.unsigned) - v.covered; {
	case trailing > 0:
		signed = fmt.Sprintf("linked, %d unsigned (%d accepted: --allow-unsigned)", len(v.unsigned), trailing)
	case v.covered > 0:
		signed = fmt.Sprintf("linked, %d unsigned (covered by a later signed entry)", v.covered)
	}
	fmt.Printf("%s: %s, %s; logs, scripts and approvals as recorded\n", p.Paint(ui.Green+ui.Bold, "VERIFIED"), plural(len(links), "entry"), signed)
	for _, c := range caveats {
		fmt.Println("  not checked: " + c)
	}
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
	Anchored   string            `yaml:"anchored"`
	Authorized string            `yaml:"authorized"`
	Details    map[string]string `yaml:"details"` // level -> its line
	Note       string            `yaml:"note"`
	Broken     []string          `yaml:"broken"`
	Unsigned   string            `yaml:"unsigned,omitempty"` // unsigned entries nothing vouches for
	Amended    []string          `yaml:"amended"`            // gaps an amend entry acknowledged
	Warnings   []string          `yaml:"warnings"`
	Notes      []string          `yaml:"notes"`
	Files      []string          `yaml:"approval_files"`
}

func (v *verifier) writeYAML(links []chain.Link, levels []level, ok bool) error {
	d := verifyDoc{Schema: "swim.verify/v1", Chain: rel(v.root, chain.Path(v.root)), Entries: len(links), Rounds: v.rounds, Approvals: v.approvals,
		OK: ok, Details: map[string]string{}, Note: v.note(levels),
		Broken: nonNil(v.bad), Unsigned: v.unsignedBad, Amended: nonNil(v.amended), Warnings: nonNil(v.warn), Notes: nonNil(v.notes), Files: nonNil(v.files)}
	for _, l := range levels {
		d.Details[l.Name] = l.Detail
		switch l.Name {
		case "consistent":
			d.Consistent = l.Result
		case "trusted":
			d.Trusted = l.Result
		case "anchored":
			d.Anchored = l.Result
		case "authorized":
			d.Authorized = l.Result
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
		who := "a key not in lookup.yml"
		if name, ok := v.lk.Who(l.Signer); ok {
			who = name
		}
		for _, a := range l.Amended {
			if amendable(a) {
				by[a] = fmt.Sprintf("acknowledged by %s, %s, in entry #%d on %s; not the original signer", who, sign.Fingerprint(l.Signer), l.Seq, pinDate(l.At))
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
	who := sign.Fingerprint(key.Public)
	if name, ok := v.lk.Who(key.Public); ok {
		who = name + ", " + who
	}
	fmt.Printf("swim verify --force: amended %s, signed by %s (not the original signers):\n", plural(len(pending), "gap"), who)
	for _, p := range pending {
		fmt.Println("  " + p)
	}
	fmt.Println()
	return true, nil
}
