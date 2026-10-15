package cli

import (
	"fmt"
	"time"

	"github.com/gates-brightly/swimlane/internal/config"
)

// Recurring schedules (swim schedule ... --daily) come in a later change.

const recurringPrefix = "r-"

type recurring struct{}

func (r *recurring) line(now time.Time) string { return "" }

func loadRecurring(root string) ([]*recurring, error) { return nil, nil }

func recurringAdd(root string, cfg *config.Config, lanes []int, when string, loc *time.Location, f scheduleFlags) error {
	return fmt.Errorf("--daily isn't supported yet")
}

func recurringRm(root, id string) error { return fmt.Errorf("no recurring schedule %s", id) }

func scheduleSetPaused(pause bool, args []string) error {
	return fmt.Errorf("pause and resume are for recurring schedules (--daily), not supported yet")
}
