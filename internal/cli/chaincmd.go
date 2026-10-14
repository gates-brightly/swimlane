package cli

import (
	"fmt"
	"os"

	"github.com/gates-brightly/swimlane/internal/anchor"
	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
)

// cmdChain is swim chain SUB: maintenance of the chain of custody itself
// (swim verify proves it, swim history reads it).
func cmdChain(args []string) error {
	if len(args) == 0 {
		return usagef("needs a subcommand: repair, checkpoint or export")
	}
	switch args[0] {
	case "repair":
		return chainRepair(args[1:])
	case "checkpoint":
		return chainCheckpoint(args[1:])
	case "export":
		return chainExport(args[1:])
	}
	return usagef("unknown subcommand %q", args[0])
}

// chainRepair removes a half-written final entry, keeping its bytes in a
// .bak file and recording the removal as a signed repair entry.
func chainRepair(args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim chain repair")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	key, err := runnerKey(cfg)
	if err != nil {
		return err
	}
	r, err := chain.Repair(root, key)
	if err != nil && key != nil && cfg.SigningMode() != config.SigningRequired {
		// As record: a key that couldn't sign leaves the chain untouched,
		// so repair it with an unsigned (still linked) entry.
		fmt.Fprintln(os.Stderr, "swim: warning: chain of custody: couldn't sign ("+err.Error()+"); recording the repair unsigned")
		r, err = chain.Repair(root, nil)
	}
	if err != nil {
		return err
	}
	if r.Bytes == 0 {
		fmt.Println("nothing to repair: " + rel(root, chain.Path(root)) + " ends with a complete entry")
		return nil
	}
	fmt.Printf("removed %d bytes of an incomplete final entry (sha256 %s)\n", r.Bytes, short12(r.SHA256))
	fmt.Printf("kept in %s; recorded as repair entry %s\n", rel(root, r.Backup), short12(r.Hash))
	return nil
}

// chainCheckpoint signs the chain's head now and sends it to the
// configured anchors (and --to DEST).
func chainCheckpoint(args []string) error {
	var to string
	rest, err := flags{strs: map[string]*string{"to": &to}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("usage: swim chain checkpoint [--to DEST]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	var extra []string
	if to != "" {
		extra = []string{to}
	}
	cp, err := takeCheckpoint(root, cfg, true, extra...)
	if err != nil {
		return err
	}
	if cp == nil {
		fmt.Println("the chain is empty: nothing to checkpoint")
		return nil
	}
	signed := "unsigned"
	if cp.Sig != "" {
		signed = "signed by " + cp.RunnerKeyFP
	}
	fmt.Printf("checkpoint %s, head %s, %s\n", cp, short12(cp.Head), signed)
	sent := loadSent(root)
	specs := append(append([]string(nil), cfg.ChainAnchors...), extra...)
	if len(specs) == 0 {
		fmt.Println("no chain_anchors configured: kept only in " + rel(root, checkpointsLog(root)) + ", which a lane can rewrite")
	}
	failed := false
	for _, s := range specs {
		if sent[s] == cp.Seq {
			fmt.Println("sent to " + s)
		} else {
			failed = true
		}
	}
	if failed {
		return exitError{1}
	}
	return nil
}

// chainExport copies the chain's entries that DEST doesn't hold yet, then
// a checkpoint of the head. DEST's entries must be the local chain's
// first ones; if they aren't, nothing is written.
func chainExport(args []string) error {
	var to string
	rest, err := flags{strs: map[string]*string{"to": &to}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 0 || to == "" {
		return usagef("usage: swim chain export --to DEST (file:/path)")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	a, err := openAnchor(root, to)
	if err != nil {
		return err
	}
	n, err := anchor.Export(a, chain.Path(root))
	if err != nil {
		return err
	}
	fmt.Printf("exported %d new entries to %s\n", n, to)
	cp, err := takeCheckpoint(root, cfg, true, to)
	if err != nil {
		return err
	}
	if cp != nil && loadSent(root)[to] == cp.Seq {
		fmt.Printf("and checkpoint %s\n", cp)
	}
	return nil
}
