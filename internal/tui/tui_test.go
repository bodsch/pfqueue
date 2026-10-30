package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"pfqueue/internal/backend/fakebe"
	"pfqueue/internal/pfq"
)

var sample = []fakebe.Msg{
	{ID: "A1", From: "carol@x", To: "bob@y", Subject: "zeta", Body: "line1\n\tindented\r\n"},
	{ID: "B2", From: "alice@x", To: "dave@y", Subject: "alpha"},
	{ID: "C3", From: "bob@x", To: "erin@y", Subject: "spam"},
}

// newUI returns a UI on an 80x24 simulation screen and a fake backend.
func newUI(t *testing.T, msgs []fakebe.Msg) (*UI, tcell.SimulationScreen, *fakebe.Backend) {
	t.Helper()
	be := fakebe.New(slices.Clone(msgs))
	conf := pfq.DefaultConfig()
	conf.ScanDelay = 3600
	c := pfq.New(conf, pfq.DefaultStatus(), nil)
	if err := c.StartWith(be); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)

	s := tcell.NewSimulationScreen("UTF-8")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Fini)
	s.SetSize(80, 24)
	u := &UI{c: c, s: s, mark: -1, bodyHeight: 10}
	return u, s, be
}

// row returns the text of screen line y without trailing blanks.
func row(s tcell.Screen, y int) string {
	w, _ := s.Size()
	var b strings.Builder
	for x := range w {
		str, _, _ := s.Get(x, y)
		if str == "" {
			str = " "
		}
		b.WriteString(str)
	}
	return strings.TrimRight(b.String(), " ")
}

func screenText(s tcell.Screen) string {
	_, h := s.Size()
	var lines []string
	for y := range h {
		lines = append(lines, row(s, y))
	}
	return strings.Join(lines, "\n")
}

func tagged(u *UI) []string {
	var out []string
	for _, m := range u.c.Snapshot() {
		if m.Tagged {
			out = append(out, m.ID)
		}
	}
	return out
}

func TestSplitBody(t *testing.T) {
	got := splitBody("a\tb\r\n12345678\tc\n")
	want := []string{"a       b", "12345678        c"}
	if !slices.Equal(got, want) {
		t.Fatalf("splitBody = %q", got)
	}
}

func TestPutReplacesControlCharacters(t *testing.T) {
	u, s, _ := newUI(t, nil)
	u.put(0, 0, "a\x1b[2Jb\tc\u0085d", tcell.StyleDefault, -1)
	if got := row(s, 0); got != "a?[2Jb c?d" {
		t.Fatalf("row = %q", got)
	}
	u.put(0, 1, "truncated", tcell.StyleDefault, 4)
	if got := row(s, 1); got != "trun" {
		t.Fatalf("row = %q", got)
	}
}

func TestDrawMainScreen(t *testing.T) {
	u, s, _ := newUI(t, sample)
	u.draw()
	text := screenText(s)
	for _, want := range []string{"Queue: 'q0', 3 messages, 0 tagged", "carol@x", "Message: A1", "Subj..: zeta"} {
		if !strings.Contains(text, want) {
			t.Errorf("screen lacks %q:\n%s", want, text)
		}
	}
}

func TestDrawSmallAndEmpty(t *testing.T) {
	u, s, _ := newUI(t, nil)
	u.showBodyWin = true
	for _, size := range [][2]int{{80, 24}, {10, 3}, {1, 1}} {
		s.SetSize(size[0], size[1])
		u.draw() // must not panic
	}
	s.SetSize(80, 24)
	u.draw()
	if !strings.Contains(screenText(s), "Queue is empty") {
		t.Fatal("empty queue not shown")
	}
}

func TestNavigationAndTagging(t *testing.T) {
	u, _, _ := newUI(t, sample)
	u.refreshData()

	u.handleRune('t') // tag A1, move to B2
	if u.cur != 1 || !slices.Equal(tagged(u), []string{"A1"}) {
		t.Fatalf("cur %d tagged %v", u.cur, tagged(u))
	}
	u.handleRune('m') // mark B2 (tags it)
	u.handleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	u.handleRune('t') // tag B2..C3
	if !slices.Equal(tagged(u), []string{"A1", "B2", "C3"}) || u.mark != -1 {
		t.Fatalf("block tag = %v, mark %d", tagged(u), u.mark)
	}
	u.handleRune('u')
	if len(tagged(u)) != 0 {
		t.Fatal("untag all")
	}

	u.handleRune('G')
	if u.cur != 2 {
		t.Fatalf("G: cur %d", u.cur)
	}
	u.handleRune('g')
	if u.cur != 0 {
		t.Fatalf("g: cur %d", u.cur)
	}
	u.handleKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	if u.cur != 0 {
		t.Fatalf("up at top: cur %d", u.cur)
	}

	// the fake backend has a single queue: '2' must be ignored
	u.handleRune('2')
	if u.c.Status().CurQueue != 0 {
		t.Fatal("switched to a missing queue")
	}

	u.handleRune('q')
	if !u.quit {
		t.Fatal("q does not quit")
	}
}

func TestDeleteAsksForConfirmation(t *testing.T) {
	u, s, be := newUI(t, sample)
	u.refreshData()

	s.InjectKey(tcell.KeyRune, 'n', tcell.ModNone)
	u.handleRune('d')
	if len(be.Actions) != 0 {
		t.Fatalf("deleted without confirmation: %v", be.Actions)
	}

	s.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
	u.handleRune('d')
	if !slices.Equal(be.Actions, []string{"delete A1"}) {
		t.Fatalf("actions = %v", be.Actions)
	}

	// the rescan after an action runs in the background
	deadline := time.Now().Add(5 * time.Second)
	for u.c.Len() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("deleted message still listed")
		}
		time.Sleep(10 * time.Millisecond)
	}

	u.handleRune('c') // confirmation off
	u.refreshData()
	u.handleKey(tcell.NewEventKey(tcell.KeyDelete, 0, tcell.ModNone))
	if !slices.Equal(be.Actions, []string{"delete A1", "delete B2"}) {
		t.Fatalf("actions = %v", be.Actions)
	}
}

func TestAskSort(t *testing.T) {
	u, s, _ := newUI(t, sample)
	for _, tc := range []struct {
		key  rune
		f    pfq.SortField
		desc bool
		ok   bool
	}{
		{'0', pfq.SortUnsorted, false, true},
		{'2', pfq.SortTo, false, true},
		{'6', pfq.SortSubject, true, true},
		{'7', 0, false, false},
	} {
		s.InjectKey(tcell.KeyRune, tc.key, tcell.ModNone)
		f, desc, ok := u.askSort()
		if f != tc.f || desc != tc.desc || ok != tc.ok {
			t.Errorf("key %c = %v %v %v", tc.key, f, desc, ok)
		}
	}
}

func TestPromptAndSearch(t *testing.T) {
	u, s, _ := newUI(t, sample)
	u.refreshData()
	for _, r := range "s:spa" {
		s.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	s.InjectKey(tcell.KeyBackspace2, 0, tcell.ModNone)
	s.InjectKey(tcell.KeyRune, 'a', tcell.ModNone)
	s.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	u.handleRune('/')
	if u.cur != 2 {
		t.Fatalf("search moved to %d", u.cur)
	}

	s.InjectKey(tcell.KeyRune, 'x', tcell.ModNone)
	s.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	if text, ok := u.prompt("title"); ok || text != "" {
		t.Fatalf("escape = %q %v", text, ok)
	}
}

func TestBodyPopup(t *testing.T) {
	u, s, _ := newUI(t, sample)
	u.refreshData()
	s.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
	u.handleRune('s')
	if got := u.body.lines; !slices.Equal(got, []string{"line1", "        indented"}) {
		t.Fatalf("body lines = %q", got)
	}
}

func TestHelpPages(t *testing.T) {
	u, s, _ := newUI(t, sample)
	s.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	s.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	s.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
	u.help()
	// the last page drawn before q is the backend page
	if text := screenText(s); !strings.Contains(text, "Backend ID: fake") {
		t.Fatalf("help page 3 not shown:\n%s", text)
	}
}
