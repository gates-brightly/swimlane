package display

import (
	"os"
	"testing"
)

// The lane palette depends on the terminal (ui.Colors256): the tests use
// the 16-colour one, so the goldens are the same on any machine.
func TestMain(m *testing.M) {
	os.Setenv("SWIM_COLORS", "16")
	os.Exit(m.Run())
}
