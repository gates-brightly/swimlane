package display

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func filled(n int) *viewport {
	v := newViewport(Scrollback)
	for i := 1; i <= n; i++ {
		v.add(vline{text: fmt.Sprintf("line %d", i)})
	}
	return v
}

func TestViewportFollowPauseCount(t *testing.T) {
	v := filled(100)
	if got := v.render(3, 40); !reflect.DeepEqual(got, []string{"line 98", "line 99", "line 100"}) {
		t.Fatalf("following: %q", got)
	}
	v.up(1, 3, 40)
	if v.follow || v.render(3, 40)[0] != "line 97" {
		t.Fatalf("up: follow=%v %q", v.follow, v.render(3, 40))
	}
	// Paused, the view stays put while lines arrive, and counts them.
	for i := 101; i <= 137; i++ {
		v.add(vline{text: fmt.Sprintf("line %d", i)})
	}
	if v.newLines != 37 || v.render(3, 40)[0] != "line 97" {
		t.Fatalf("paused: new=%d %q", v.newLines, v.render(3, 40))
	}
	v.up(10, 3, 40)
	if v.render(3, 40)[0] != "line 87" {
		t.Fatalf("page up: %q", v.render(3, 40))
	}
	v.home(3, 40)
	if v.render(3, 40)[0] != "line 1" {
		t.Fatalf("home: %q", v.render(3, 40))
	}
	// Scrolling down to the bottom follows again.
	v.top = 133
	v.down(1, 3, 40)
	if !v.follow || v.newLines != 0 || v.render(3, 40)[2] != "line 137" {
		t.Fatalf("down to the end: follow=%v new=%d %q", v.follow, v.newLines, v.render(3, 40))
	}
	v.up(5, 3, 40)
	v.toEnd()
	if !v.follow {
		t.Fatal("End doesn't follow")
	}
}

func TestViewportShortAndWrapped(t *testing.T) {
	v := filled(2)
	v.up(1, 5, 40)
	if !v.follow {
		t.Fatal("everything fits: up should do nothing")
	}
	if got := v.render(3, 40); !reflect.DeepEqual(got, []string{"line 1", "line 2", ""}) {
		t.Fatalf("short: %q", got)
	}
	// A wrapped line keeps its prefix on every row.
	w := newViewport(10)
	w.add(vline{prefix: "[3] ", prefixW: 4, text: "abcdefghij"})
	if got := w.render(3, 8); !reflect.DeepEqual(got, []string{"[3] abcd", "[3] efgh", "[3] ij"}) {
		t.Fatalf("wrapped: %q", got)
	}
}

func TestViewportDropsOldest(t *testing.T) {
	v := newViewport(80)
	for i := 1; i <= 50; i++ {
		v.add(vline{text: fmt.Sprint(i)})
	}
	v.up(1, 3, 20)
	top := v.top
	for i := 51; i <= 200; i++ {
		v.add(vline{text: fmt.Sprint(i)})
	}
	if len(v.lines) > 90 || v.base == 0 {
		t.Fatalf("kept %d lines from %d", len(v.lines), v.base)
	}
	if v.top < v.base || v.top == top {
		t.Fatalf("paused view must move to the oldest kept line: top %d base %d", v.top, v.base)
	}
	if got := v.render(1, 20)[0]; got != fmt.Sprint(v.base+1) {
		t.Fatalf("got %q at base %d", got, v.base)
	}
}

func testTUI(t *testing.T) (*tui, []LaneView) {
	dir := t.TempDir()
	m := newTUI(false, func(n int) string { return filepath.Join(dir, fmt.Sprintf("agent%d.log", n)) },
		func(n int) string { return fmt.Sprintf(".swim/logs/agent%d.log", n) })
	views, _ := fixture()
	return m, views
}

func press(m *tui, views []LaneView, keys ...Key) {
	for _, k := range keys {
		m.key(k, views)
	}
}

var (
	right = Key{Code: KeyRight}
	left  = Key{Code: KeyLeft}
	esc   = Key{Code: KeyEsc}
	enter = Key{Code: KeyEnter}
)

func r(c rune) Key { return Key{Code: KeyRune, Rune: c} }

func TestTUILaneOrder(t *testing.T) {
	m, views := testTUI(t)
	// Running, then waiting, then finished; idle lanes aren't in the run.
	if got := m.order(views); !reflect.DeepEqual(got, []int{1, 2, 4, 5, 6, 7}) {
		t.Fatalf("order %v", got)
	}
	m.filter = filterFailed
	if got := m.order(views); !reflect.DeepEqual(got, []int{6}) {
		t.Fatalf("failed %v", got)
	}
	m.filter = filterRunning
	if got := m.order(views); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("running %v", got)
	}
}

func TestTUISelection(t *testing.T) {
	m, views := testTUI(t)
	press(m, views, right)
	if m.laneN != 1 {
		t.Fatalf("→ from all: swim %d", m.laneN)
	}
	press(m, views, right, r('l'), Key{Code: KeyTab})
	if m.laneN != 5 {
		t.Fatalf("→ ×3: swim %d", m.laneN)
	}
	press(m, views, left, r('h'))
	if m.laneN != 2 {
		t.Fatalf("← ×2: swim %d", m.laneN)
	}
	press(m, views, esc)
	if m.laneN != 0 || !m.all.follow {
		t.Fatal("Esc returns to the all view")
	}
	press(m, views, right)
	if m.laneN != 2 {
		t.Fatalf("→ reopens the last lane: swim %d", m.laneN)
	}
	press(m, views, esc, left)
	if m.laneN != 7 {
		t.Fatalf("← from all opens the last lane: swim %d", m.laneN)
	}
	// The selection survives the lane changing state (and order).
	views[6].State = Running
	press(m, views, right)
	if m.laneN != 2 {
		t.Fatalf("after 7 started, → moves on from it (order 1 7 2 4 5 6): swim %d", m.laneN)
	}
	// Digits then Enter jump to any lane in the panel, idle too.
	press(m, views, r('3'), enter)
	if m.laneN != 3 {
		t.Fatalf("3 Enter: swim %d", m.laneN)
	}
	press(m, views, r('9'), r('9'), enter)
	if m.laneN != 3 || !strings.Contains(m.divider(views), "no swim 99") {
		t.Fatalf("99 Enter: swim %d, %q", m.laneN, m.divider(views))
	}
	press(m, views, r('4'), Key{Code: KeyBackspace}, r('6'), enter)
	if m.laneN != 6 {
		t.Fatalf("4 ⌫ 6 Enter: swim %d", m.laneN)
	}
	// f narrows ← →; Esc clears it.
	press(m, views, r('f'), r('f'), right)
	if m.laneN != 6 || !strings.Contains(m.divider(views), "lanes: failed") {
		t.Fatalf("failed filter: swim %d %q", m.laneN, m.divider(views))
	}
	press(m, views, esc)
	if m.filter != filterEvery {
		t.Fatal("Esc clears the filter")
	}
}

func TestTUIInterruptAndQuit(t *testing.T) {
	m, views := testTUI(t)
	if a := m.key(Key{Code: KeyCtrlC}, views); a != actInterrupt {
		t.Fatalf("Ctrl-C during the run: %v", a)
	}
	if a := m.key(r('q'), views); a != actNone {
		t.Fatalf("q during the run: %v", a)
	}
	if m.holds() {
		t.Fatal("following the all view doesn't hold the screen")
	}
	press(m, views, right)
	if !m.holds() {
		t.Fatal("a lane view holds the screen at the end")
	}
	m.finished = true
	if !strings.Contains(m.divider(views), "run finished · q to close") {
		t.Fatalf("divider %q", m.divider(views))
	}
	if a := m.key(esc, views); a != actNone || m.laneN != 0 {
		t.Fatal("Esc from a lane view goes to the all view first")
	}
	if a := m.key(esc, views); a != actQuit {
		t.Fatal("then Esc closes")
	}
	if a := m.key(r('q'), views); a != actQuit {
		t.Fatal("q closes")
	}
	if a := m.key(Key{Code: KeyCtrlC}, views); a != actQuit {
		t.Fatal("Ctrl-C closes")
	}
}

func TestTUIDividerPaused(t *testing.T) {
	m, views := testTUI(t)
	for i := 0; i < 50; i++ {
		m.addLine(1, fmt.Sprint("out ", i))
	}
	m.render("t", views, nowFixture(), 20, 80)
	press(m, views, Key{Code: KeyUp})
	for i := 0; i < 37; i++ {
		m.addLine(2, "more")
	}
	if d := m.divider(views); d != "all lanes · paused · 37 new lines · End to follow" {
		t.Fatalf("divider %q", d)
	}
	press(m, views, r('G'))
	if d := m.divider(views); d != "all lanes · following" {
		t.Fatalf("divider %q", d)
	}
}

func nowFixture() time.Time { _, now := fixture(); return now }

func TestTUIFrameGolden(t *testing.T) {
	m, views := testTUI(t)
	_, now := fixture()
	log := "# swim lane log | syntax 2 | swim 6\n\n== ROUND 2026-10-09T11:00:00Z  job=old  Deploy (yesterday)\n  PASS  old step\n" +
		"\n== ROUND 2026-10-09T11:59:30Z  job=6e1f  Deploy\n   run: r-1 | script: lane.6.sh\n\n-- stage check  11:59:30\n" +
		"  FAIL  deploy (exit 2)                             30.0s  12:00:00\n        $ ./deploy.sh\n        | Error: nope\n" +
		"\n== END FAIL  pass=0 fail=1 skip=0 drift=0  exit=2  30.0s  2026-10-09T12:00:00Z\n"
	os.WriteFile(m.logPath(6), []byte(log), 0o644)
	for i := 1; i <= 4; i++ {
		m.addLine(i%2+1, fmt.Sprintf("output line %d from a lane that has quite a lot to say about what it is doing", i))
	}
	m.addLine(0, "a launcher message")
	title := "swim · app · main@abc1234 · 1:12"
	golden(t, "tui_all.golden", strings.Join(m.render(title, views, now, 18, 80), "\n")+"\n")
	press(m, views, r('6'), enter)
	frame := m.render(title, views, now, 18, 80)
	golden(t, "tui_lane.golden", strings.Join(frame, "\n")+"\n")
	if strings.Contains(strings.Join(frame, "\n"), "yesterday") {
		t.Error("the lane view shows only the current round")
	}
	press(m, views, r('?'))
	golden(t, "tui_help.golden", strings.Join(m.render(title, views, now, 18, 80), "\n")+"\n")
	for i, l := range frame {
		if n := utf8.RuneCountInString(l); n > 79 {
			t.Errorf("row %d is %d columns: %q", i, n, l)
		}
	}
	// Coloured frames have the same text.
	m.color = true
	m.help = false
	for i, l := range m.render(title, views, now, 18, 80) {
		if stripANSI(l) != frame[i] {
			t.Errorf("row %d: colour %q, plain %q", i, stripANSI(l), frame[i])
		}
	}
}

// With more lanes than the panel holds, the selected lane keeps a row.
func TestTUISelectedLaneAlwaysShown(t *testing.T) {
	m, _ := testTUI(t)
	var views []LaneView
	for n := 1; n <= 40; n++ {
		views = append(views, LaneView{N: n, State: Passed})
	}
	views[0].State = Running
	press(m, views, r('3'), r('5'), enter)
	frame := strings.Join(m.render("t", views, nowFixture(), 30, 80), "\n")
	if !strings.Contains(frame, "▶swim 35") || !strings.Contains(frame, " swim 1 ") || !strings.Contains(frame, "… 26 more") {
		t.Fatalf("panel:\n%s", frame)
	}
}

func TestLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent1.log")
	tl := newLogTail(path)
	if lines, reset := tl.poll(); lines != nil || reset {
		t.Fatalf("no file: %q %v", lines, reset)
	}
	write := func(s string) {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		f.WriteString(s)
		f.Close()
	}
	write("# swim lane log\n\n== ROUND a  job=1  one\n  PASS  x\n\n== ROUND b  job=2  two\n  PASS  y\n  FA")
	lines, reset := tl.poll()
	if !reset || !reflect.DeepEqual(lines, []string{"== ROUND b  job=2  two", "  PASS  y"}) {
		t.Fatalf("first poll: %q %v", lines, reset)
	}
	write("IL  z\n")
	lines, reset = tl.poll()
	if reset || !reflect.DeepEqual(lines, []string{"  FAIL  z"}) {
		t.Fatalf("partial line completed: %q %v", lines, reset)
	}
	if lines, _ := tl.poll(); len(lines) != 0 {
		t.Fatalf("nothing new: %q", lines)
	}
	write("\n== ROUND c  job=3  three\n")
	lines, reset = tl.poll()
	if !reset || !reflect.DeepEqual(lines, []string{"== ROUND c  job=3  three"}) {
		t.Fatalf("new round: %q %v", lines, reset)
	}
	os.WriteFile(path, []byte("== ROUND d  job=4  four\n"), 0o644)
	lines, reset = tl.poll()
	if !reset || !reflect.DeepEqual(lines, []string{"== ROUND d  job=4  four"}) {
		t.Fatalf("replaced file: %q %v", lines, reset)
	}
}

// A terminal resized very small still gets a frame of its size.
func TestTUITinyTerminal(t *testing.T) {
	m, views := testTUI(t)
	m.addLine(1, "x")
	for _, size := range [][2]int{{3, 10}, {1, 1}, {12, 25}} {
		if got := m.render("title", views, nowFixture(), size[0], size[1]); len(got) != size[0] {
			t.Errorf("%dx%d: %d rows", size[0], size[1], len(got))
		}
	}
}
