package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gates-brightly/swimlane/internal/sign"
	"github.com/gates-brightly/swimlane/internal/trust"
)

// keysPin is swim keys pin: trust trust.yml as it is now, here (the pin
// in ~/.config/swim/trust-pins.yml), forgetting keys earlier pins kept.
func keysPin(args []string) error {
	if len(args) > 0 {
		return usagef("usage: swim keys pin")
	}
	root, _, err := repoNoMigrate()
	if err != nil {
		return err
	}
	tf, err := trust.Load(root)
	if err != nil {
		return err
	}
	if tf == nil {
		return errors.New("no trust.yml in this repo: list the keys to trust first (swim keys trust KEY)")
	}
	pin, err := trust.SavePin(root, tf.Keys, nil)
	if err != nil {
		return err
	}
	fmt.Printf("pinned trust.yml (%d keys, sha256 %s) in %s\n", len(pin.Keys), short12(pin.SHA), trust.PinsPath())
	fmt.Printf("CI, with no pins of its own: swim verify --trust-pin %s (or %s)\n", pin.SHA, trust.PinEnv)
	return nil
}

// keysTrust is swim keys trust [KEY] [--remove] [--sign] [--key K]: add
// KEY (a public key, or a .pub file; default this machine's signing key)
// to trust.yml, or remove it. --sign records the change in trust.yml.sig,
// signed with --key (a key trust.yml already trusts), so every operator's
// pin follows it; an unsigned change fails swim verify's trusted level
// until each operator pins it again.
func keysTrust(args []string) error {
	var remove, signed bool
	var keyPath string
	rest, err := flags{bools: map[string]*bool{"remove": &remove, "sign": &signed}, strs: map[string]*string{"key": &keyPath}}.parse(args)
	if err != nil {
		return err
	}
	if len(rest) > 1 || remove && len(rest) == 0 {
		return usagef("usage: swim keys trust [KEY | FILE.pub] [--remove] [--sign] [--key K]")
	}
	root, cfg, err := repoNoMigrate()
	if err != nil {
		return err
	}
	var pub string
	switch {
	case len(rest) == 1 && len(strings.Fields(rest[0])) >= 2:
		pub = strings.TrimSpace(rest[0])
	case len(rest) == 1:
		data, err := os.ReadFile(rest[0])
		if err != nil {
			return fmt.Errorf("%s: %w (give a public key or a .pub file)", rest[0], err)
		}
		pub = strings.TrimSpace(string(data))
	default:
		k, err := sign.LoadKey(cfg.SigningKey)
		if err != nil {
			return err
		}
		pub = k.Public
	}
	if len(strings.Fields(pub)) < 2 {
		return fmt.Errorf("%q isn't an SSH public key", pub)
	}
	tf, err := trust.Load(root)
	if err != nil {
		return err
	}
	fresh := tf == nil
	if fresh {
		tf = &trust.File{}
	}
	before := append([]string(nil), tf.Keys...)
	fp := sign.Fingerprint(pub)
	if remove {
		if !tf.Has(pub) {
			return fmt.Errorf("%s isn't in trust.yml", fp)
		}
		var keep []string
		for _, k := range tf.Keys {
			if sign.KeyOnly(k) != sign.KeyOnly(pub) {
				keep = append(keep, k)
			}
		}
		tf.Keys = keep
	} else {
		if tf.Has(pub) {
			fmt.Printf("%s is already in trust.yml\n", fp)
			return nil
		}
		tf.Keys = append(tf.Keys, pub)
	}
	if signed {
		if fresh {
			return errors.New("there is no trust.yml yet, so no key to sign the change with: create it without --sign (swim verify pins it on first use)")
		}
		key, err := sign.LoadKey(or(keyPath, cfg.SigningKey))
		if err != nil {
			return err
		}
		if err := trust.Sign(root, before, tf.Keys, key); err != nil {
			return err
		}
	}
	if err := tf.Save(root); err != nil {
		return err
	}
	verb := "added"
	if remove {
		verb = "removed"
	}
	fmt.Printf("%s %s %s trust.yml (%d keys, sha256 %s)\n", verb, fp, map[bool]string{true: "from", false: "to"}[remove], len(tf.Keys), short12(tf.SHA()))
	pin, _ := trust.LoadPin(root)
	switch {
	case signed:
		fmt.Println("signed in trust.yml.sig: commit both; swim verify moves each pin along")
	case pin != nil && pin.SHA != tf.SHA():
		fmt.Println("unsigned: swim verify reports trusted: no until each operator pins it again (swim keys pin), or sign it (--sign)")
	}
	return nil
}

// trustLine is swim keys' line about trust.yml.
func trustLine(root string) string {
	tf, err := trust.Load(root)
	if err != nil {
		return "trust.yml: " + err.Error()
	}
	if tf == nil {
		return "no trust.yml: swim verify can't tell a chain re-signed with another key (swim keys trust)"
	}
	pin, err := trust.LoadPin(root)
	if err != nil {
		return "trust.yml: " + err.Error()
	}
	res := trust.Check(root, tf, pin)
	state := map[trust.Status]string{
		trust.TOFU:      "not pinned yet: swim verify (or swim keys pin) pins it",
		trust.Match:     "pinned",
		trust.Advanced:  fmt.Sprintf("%d signed changes past the pin", res.Steps),
		trust.Unsigned:  "changed since the pin, unsigned: " + res.Why,
		trust.Malformed: res.Why,
	}[res.Status]
	return fmt.Sprintf("trust.yml: %d keys, sha256 %s; %s", len(tf.Keys), short12(tf.SHA()), state)
}
