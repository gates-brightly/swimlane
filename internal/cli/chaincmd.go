package cli

import (
	"fmt"
	"os"

	"github.com/gates-brightly/swimlane/internal/chain"
	"github.com/gates-brightly/swimlane/internal/config"
)

// cmdChain is swim chain SUB: maintenance of the chain of custody itself
// (swim verify proves it, swim history reads it).
func cmdChain(args []string) error {
	if len(args) == 0 {
		return usagef("needs a subcommand: repair")
	}
	switch args[0] {
	case "repair":
		return chainRepair(args[1:])
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
