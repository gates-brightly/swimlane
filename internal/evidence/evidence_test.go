package evidence

import (
	"strings"
	"testing"
)

func TestNormalizeAndRecord(t *testing.T) {
	if Normalize("a  \r\nb\t\r\n\n\n") != "a\nb\n" {
		t.Errorf("normalize: %q", Normalize("a  \r\nb\t\r\n\n\n"))
	}
	root := t.TempDir()
	h1, err := Record(root, "1", "job-1", "r-1", "sha", "tf plan", "1 to destroy  \n", false)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := Record(root, "1", "job-1", "r-1", "sha", "db diff", "none\n", false)
	if h1 == h2 {
		t.Error("a second step didn't change the round's hash")
	}
	ix := Load(root, "job-1")
	if ix == nil || len(ix.Steps) != 2 || ix.Hash != h2 || ix.Steps[0].Label != "db diff" {
		t.Fatalf("index %+v", ix)
	}
	text, _ := ix.Text(root)
	if text != "== db diff\nnone\n== tf plan\n1 to destroy\n" || Blob(root, h2) != text {
		t.Errorf("text %q", text)
	}
	// A new run starts afresh; the lane index follows.
	h3, _ := Record(root, "1", "job-1", "r-2", "sha", "tf plan", "1 to destroy\n", true)
	if h3 != h1 || len(Load(root, "job-1").Steps) != 1 || LoadLane(root, "1").Run != "r-2" || !LoadLane(root, "1").Check {
		t.Errorf("new run: %s %+v", h3, Load(root, "job-1"))
	}
}

func TestDiff(t *testing.T) {
	d := strings.Join(Diff("a\nb\nc\n", "a\nc\nd\n", 10), "\n")
	if d != "- b\n+ d" {
		t.Errorf("diff:\n%s", d)
	}
	if got := Diff(strings.Repeat("x\n", 50), strings.Repeat("y\n", 50), 5); len(got) != 6 || !strings.Contains(got[5], "more changes") {
		t.Errorf("capped diff: %q", got)
	}
}
