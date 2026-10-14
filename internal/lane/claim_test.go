package lane

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gates-brightly/swimlane/internal/proc"
)

// A pid file names a process by pid and start time: the same pid with
// another start time (the pid handed to a new process) isn't the lane.
func TestRunningChecksStartTime(t *testing.T) {
	root := t.TempDir()
	if err := WritePID(root, 1, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(PIDFile(root, 1))
	if pid, start, ok := proc.Parse(string(data)); !ok || pid != os.Getpid() || start == "" {
		t.Fatalf("pid file %q: want pid and start time", data)
	}
	if _, ok := Running(root, 1); !ok {
		t.Fatal("own claim not running")
	}
	os.WriteFile(PIDFile(root, 1), []byte(fmt.Sprintf("%d 1\n", os.Getpid())), 0o644)
	if _, ok := Running(root, 1); ok {
		t.Fatal("a live pid with the wrong start time reported running")
	}
	// A bare pid (written by an older swim) falls back to the pid alone.
	os.WriteFile(PIDFile(root, 1), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
	if _, ok := Running(root, 1); !ok {
		t.Fatal("legacy pid file of a live process not running")
	}
	// A reused pid doesn't block a new claim.
	os.WriteFile(PIDFile(root, 1), []byte(fmt.Sprintf("%d 1\n", os.Getpid())), 0o644)
	if err := WritePID(root, 1, os.Getpid()); err != nil {
		t.Fatal(err)
	}
}

// Ten processes claiming one lane at once: exactly one wins.
func TestClaimRace(t *testing.T) {
	root := t.TempDir()
	const n = 10
	var cmds []*exec.Cmd
	var stdins []io.Closer
	results := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestClaimHelper$")
		cmd.Env = append(os.Environ(), "SWIM_TEST_CLAIM_ROOT="+root)
		in, _ := cmd.StdinPipe()
		out, _ := cmd.StdoutPipe()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds, stdins = append(cmds, cmd), append(stdins, in)
		wg.Add(1)
		go func() {
			defer wg.Done()
			line, _ := bufio.NewReader(out).ReadString('\n')
			results <- strings.TrimSpace(line)
			io.Copy(io.Discard, out)
		}()
	}
	won := 0
	for i := 0; i < n; i++ {
		r := <-results
		switch {
		case strings.HasPrefix(r, "won"):
			won++
		case strings.HasPrefix(r, "lost"):
		default:
			t.Errorf("claimer said %q", r)
		}
	}
	pid, ok := Running(root, 1)
	for _, in := range stdins {
		in.Close()
	}
	wg.Wait()
	for _, c := range cmds {
		c.Wait()
	}
	if won != 1 {
		t.Fatalf("%d claimers won, want exactly 1", won)
	}
	if !ok || pid == os.Getpid() {
		t.Fatalf("the winner isn't recorded: pid %d %v", pid, ok)
	}
	if _, ok := Running(root, 1); ok {
		t.Fatal("lane still running after its claimer exited")
	}
}

// TestClaimHelper is a claimer for TestClaimRace (run as its own process).
func TestClaimHelper(t *testing.T) {
	root := os.Getenv("SWIM_TEST_CLAIM_ROOT")
	if root == "" {
		t.Skip("run by TestClaimRace")
	}
	if err := WritePID(root, 1, os.Getpid()); err != nil {
		fmt.Println("lost " + err.Error())
		return
	}
	fmt.Println("won " + strconv.Itoa(os.Getpid()))
	io.Copy(io.Discard, os.Stdin) // hold the lane until the test is done
}
