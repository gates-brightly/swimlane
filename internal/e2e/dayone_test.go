package e2e

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gates-brightly/swimlane/internal/assets"
)

// laterWords are swim's concepts a newcomer doesn't need on day one; swim
// concepts topic introduces them after the four ideas. Day one is lane, round, step,
// stage, guard (a dry run until approved), queue and log.
var laterWords = regexp.MustCompile(`(?i)\b(swimmers?|batch(es)?|drift|decisions?|checkpoints?|earmarks?|signoffs?|claims?|stubs?|manifests?|chain|custody|lookup|signing|staged)\b`)

// dayOne drops what a reader isn't asked to understand: the syntax marker
// and the Job: line swim new writes (kept as is).
func dayOne(text string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "# swim: syntax") || strings.HasPrefix(l, "# Job:") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

func noLaterWords(t *testing.T, where, text string) {
	t.Helper()
	if m := laterWords.FindAllString(dayOne(text), -1); len(m) > 0 {
		t.Errorf("%s uses words a newcomer doesn't need yet (swim guide concepts has them): %v\n%s", where, m, text)
	}
}

// What a newcomer meets first (the README quickstart, swim init, swim new
// and its template, swim queue) uses only the day-one words.
func TestDayOneVocabulary(t *testing.T) {
	noLaterWords(t, "the README quickstart", strings.Join(quickstartBlocks(t), "\n"))
	tmpl, err := assets.Script(1, "say hello", "", "job-0001", "me")
	if err != nil {
		t.Fatal(err)
	}
	noLaterWords(t, "swim new's template", tmpl)

	r := newRepo(t, "")
	noLaterWords(t, "swim init", r.mustSwim("init"))
	noLaterWords(t, "swim new", r.mustSwim("new", "1", "say hello"))
	r.script(1, "say hello", `run "hello" echo hello`)
	noLaterWords(t, "swim queue", r.mustSwim("queue", "1"))
}
