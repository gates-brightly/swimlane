package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/gates-brightly/swimlane/internal/approval"
	"github.com/gates-brightly/swimlane/internal/queue"
)

// A batch swim watch runs alongside others (swim _batch) has no terminal:
// with nobody to ask, its guards would dry-run. When the watch screen is
// up, each batch gets a private channel back to it instead: one end of a
// socket pair, as fd 3 (swim _batch --ask), closed on exec so the batch's
// lanes never hold it. The batch's launcher decides every guard and
// decision as before (flags, signed approvals, -y); what only a person
// can answer it sends up the channel, and the screen asks the operator in
// yellow, naming the lane and the batch. Several batches may ask at once:
// the screen lists them all, oldest first.

// askFD is the channel's fd in swim _batch.
const askFD = 3

// askGone is why a question went unanswered when swim watch went away:
// nobody was asked, so the guard is a dry run, not a decline.
const askGone = "swim watch closed before anyone answered"

// askMsg is one line on the channel. The batch sends op "ask" (a question
// for lane Lane, its lines in Question) and "cancel" (its run stopped
// before an answer); swim watch replies with the answer to ID.
type askMsg struct {
	Op       string   `json:"op,omitempty"`
	ID       int      `json:"id"`
	Lane     int      `json:"lane,omitempty"`
	Flag     string   `json:"flag,omitempty"`
	Desc     string   `json:"desc,omitempty"`
	Question []string `json:"question,omitempty"`
	Choices  int      `json:"choices,omitempty"` // a decision's choice count (0: a guard)
	Approved bool     `json:"approved,omitempty"`
	Choice   int      `json:"choice,omitempty"`
	By       string   `json:"by,omitempty"`
	Why      string   `json:"why,omitempty"`
}

// askClient is the batch's end: an approval.Asker that waits for the
// operator at swim watch's screen.
type askClient struct {
	mu      sync.Mutex
	conn    net.Conn
	enc     *json.Encoder
	next    int
	waiting map[int]chan askMsg
	gone    bool // swim watch closed the channel
}

// openAskChannel takes swim _batch's inherited fd 3 (nil, err if it isn't
// a socket) and keeps it from the lanes the batch starts.
func openAskChannel() (*askClient, error) {
	syscall.CloseOnExec(askFD)
	f := os.NewFile(askFD, "swim-watch-ask")
	conn, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("swim _batch --ask: fd %d isn't swim watch's channel: %v", askFD, err)
	}
	return newAskClient(conn), nil
}

func newAskClient(conn net.Conn) *askClient {
	c := &askClient{conn: conn, enc: json.NewEncoder(conn), waiting: map[int]chan askMsg{}}
	go c.read()
	return c
}

func (c *askClient) read() {
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64<<10), approval.MaxRequest)
	for sc.Scan() {
		var m askMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		c.mu.Lock()
		ch := c.waiting[m.ID]
		delete(c.waiting, m.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- m
		}
	}
	c.mu.Lock()
	c.gone = true
	for id, ch := range c.waiting {
		ch <- askMsg{ID: id, Why: askGone}
		delete(c.waiting, id)
	}
	c.mu.Unlock()
}

// ask puts req to the operator at swim watch's screen and waits for the
// answer, or for cancel (the run is stopping).
func (c *askClient) ask(req approval.Request, cancel <-chan struct{}) approval.Response {
	q := approval.Question(req)
	if req.Choices != nil {
		q = approval.DecisionQuestion(req)
	}
	q = q[:len(q)-1] // the terminal prompt's last line: swim watch says its own keys
	ch := make(chan askMsg, 1)
	c.mu.Lock()
	if c.gone {
		c.mu.Unlock()
		return approval.Response{Dry: true, Why: askGone}
	}
	c.next++
	id := c.next
	c.waiting[id] = ch
	err := c.enc.Encode(askMsg{Op: "ask", ID: id, Lane: req.Lane, Flag: req.Flag, Desc: req.Desc, Question: q, Choices: len(req.Choices)})
	c.mu.Unlock()
	if err != nil {
		return approval.Response{Dry: true, Why: askGone + ": " + err.Error()}
	}
	select {
	case m := <-ch:
		return approval.Response{Approved: m.Approved, Choice: m.Choice, By: m.By, Why: m.Why, Dry: m.Why == askGone}
	case <-cancel:
		c.mu.Lock()
		delete(c.waiting, id)
		c.enc.Encode(askMsg{Op: "cancel", ID: id})
		c.mu.Unlock()
		return approval.Response{Why: "the run is stopping"}
	}
}

// askPair makes a batch's channel: swim watch's end, and the end the batch
// inherits as fd 3.
func askPair() (net.Conn, *os.File, error) {
	// Under ForkLock, so no other batch starting now inherits either end
	// (macOS has no SOCK_CLOEXEC).
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1]) // exec.Cmd's ExtraFiles hands it to this batch alone
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, fmt.Errorf("swim watch: approval channel: %v", err)
	}
	mine := os.NewFile(uintptr(fds[0]), "swim-watch-ask")
	conn, err := net.FileConn(mine)
	mine.Close()
	if err != nil {
		syscall.Close(fds[1])
		return nil, nil, fmt.Errorf("swim watch: approval channel: %v", err)
	}
	return conn, os.NewFile(uintptr(fds[1]), "swim-watch-ask-batch"), nil
}

// watchAsk is a question a batch put to swim watch's screen.
type watchAsk struct {
	key   string // "ask:<batch id>:<id>", the screen row's ID
	seq   int    // the batch's queue number
	batch string // the batch's entry id
	msg   askMsg
	since time.Time
	reply func(askMsg)
}

// watchAsks are the questions waiting at swim watch's screen, oldest
// first, from every batch it runs.
type watchAsks struct {
	mu    sync.Mutex
	list  []*watchAsk
	added int       // questions ever added: the screen selects each new one
	bell  io.Writer // rung for each new question (nil: silent)
	// changed, if set, is called when a question comes or goes (tests
	// wait on it).
	changed func()
}

func (a *watchAsks) notify() {
	if a.changed != nil {
		a.changed()
	}
}

// lane is the lane the question with row key blocks, if it waits still.
func (a *watchAsks) lane(key string) (int, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, q := range a.list {
		if q.key == key {
			return q.msg.Lane, true
		}
	}
	return 0, false
}

// serve reads batch e's questions from conn until the batch exits.
func (a *watchAsks) serve(e *queue.Entry, conn net.Conn) {
	defer conn.Close()
	var wmu sync.Mutex
	enc := json.NewEncoder(conn)
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64<<10), approval.MaxRequest)
	for sc.Scan() {
		var m askMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		key := "ask:" + e.ID + ":" + strconv.Itoa(m.ID)
		switch m.Op {
		case "ask":
			id := m.ID
			a.mu.Lock()
			a.list = append(a.list, &watchAsk{key: key, seq: e.Seq, batch: e.ID, msg: m, since: time.Now(),
				reply: func(r askMsg) {
					r.ID = id
					wmu.Lock()
					enc.Encode(r)
					wmu.Unlock()
				}})
			a.added++
			a.mu.Unlock()
			// The bell, heard even while enter follows some output: the
			// question is first on the screen when it comes back.
			if a.bell != nil {
				fmt.Fprint(a.bell, "\a")
			}
			a.notify()
		case "cancel":
			a.drop(key)
			a.notify()
		}
	}
	// The batch is gone: its questions with it.
	a.mu.Lock()
	keep := a.list[:0]
	for _, q := range a.list {
		if q.batch != e.ID {
			keep = append(keep, q)
		}
	}
	a.list = keep
	a.mu.Unlock()
	a.notify()
}

func (a *watchAsks) drop(key string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, q := range a.list {
		if q.key == key {
			a.list = append(a.list[:i:i], a.list[i+1:]...)
			return
		}
	}
}

// pending lists the questions waiting, oldest first, and how many were
// ever added.
func (a *watchAsks) pending() ([]watchAsk, int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]watchAsk, 0, len(a.list))
	for _, q := range a.list {
		out = append(out, *q)
	}
	return out, a.added
}

// answer answers the question with row key (the oldest if key isn't one)
// with key press k: y or n for a guard, a choice's number for a decision.
// It says what happened.
func (a *watchAsks) answer(key string, k rune) string {
	a.mu.Lock()
	var q *watchAsk
	for _, p := range a.list {
		if p.key == key {
			q = p
		}
	}
	if q == nil && len(a.list) > 0 {
		q = a.list[0]
	}
	if q == nil {
		a.mu.Unlock()
		return "nothing waits for an answer"
	}
	m := q.msg
	m.Flag = approval.Clean(m.Flag)
	who := fmt.Sprintf("swim %d (#%d)", m.Lane, q.seq)
	var r askMsg
	var msg string
	switch {
	case m.Choices == 0 && (k == 'y' || k == 'n'):
		r = askMsg{Approved: k == 'y', By: operator()}
		msg = fmt.Sprintf("%s: %s declined", who, m.Flag)
		if r.Approved {
			msg = fmt.Sprintf("%s: %s approved", who, m.Flag)
		}
	case m.Choices == 0:
		a.mu.Unlock()
		return fmt.Sprintf("%s asks for approval of %s: y approves, n declines", who, m.Flag)
	case k >= '1' && k <= '9' && int(k-'0') <= m.Choices:
		c := int(k - '0')
		r = askMsg{Approved: true, Choice: c, By: operator()}
		msg = fmt.Sprintf("%s: %s = %d", who, m.Flag, c)
	case k == 'n':
		r = askMsg{Why: "declined in swim watch by " + operator()}
		msg = fmt.Sprintf("%s: %s left unanswered (the round ends there, as a dry run)", who, m.Flag)
	default:
		a.mu.Unlock()
		return fmt.Sprintf("%s asks for a decision, %s: press 1-%d (n leaves it unanswered)", who, m.Flag, m.Choices)
	}
	for i, p := range a.list {
		if p == q {
			a.list = append(a.list[:i:i], a.list[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	q.reply(r)
	return msg
}
