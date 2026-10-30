// Package tui is the interactive terminal frontend, a tcell port of the
// ncurses frontend of the C version (pfq_fencurses.c).
package tui

import (
	"time"

	"github.com/gdamore/tcell/v2"

	"pfqueue/internal/backend"
	"pfqueue/internal/pfq"
)

const (
	Name    = "ncurses"
	Version = "1.0"

	regexpLen = 200
)

// Info carries program information shown in the help window.
type Info struct {
	Version string
}

// UI is the frontend state.
type UI struct {
	c    *pfq.Context
	s    tcell.Screen
	info Info

	st     pfq.Status
	msgs   []pfq.Message // snapshot used for the last draw
	colors bool

	cur   int // current message (CURMSG)
	first int // first message shown (FIRSTMSG)
	mark  int // block mark (MSGMARK), -1 if unset

	showBodyWin    bool
	showBodyAlways bool
	bodyHeight     int
	bodyFrom       int    // first body line shown in the body window
	bodyID         string // message shown in the body window
	body           struct {
		id    string
		lines []string
	}

	quit bool
}

// Run starts the frontend and returns when the user quits.
func Run(c *pfq.Context, info Info) error {
	s, err := tcell.NewScreen()
	if err != nil {
		return err
	}
	if err := s.Init(); err != nil {
		return err
	}
	defer s.Fini()
	s.SetStyle(tcell.StyleDefault)

	u := &UI{
		c: c, s: s, info: info,
		mark:           -1,
		showBodyAlways: true,
		bodyHeight:     10,
	}
	if s.Colors() < 8 {
		c.UpdateStatus(func(st *pfq.Status) { st.UseColors = false })
	}

	stop := make(chan struct{})
	defer close(stop)
	go u.ticker(time.Duration(max(c.Conf.Refresh, 1))*time.Second, stop)

	u.loop()
	return nil
}

// ticker refreshes the screen periodically (halfdelay in the C version).
func (u *UI) ticker(d time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			// fails only when the event queue is full; then a redraw
			// is pending anyway
			_ = u.s.PostEvent(tcell.NewEventInterrupt(nil))
		case <-stop:
			return
		}
	}
}

func (u *UI) loop() {
	for !u.quit {
		u.colors = u.c.Status().UseColors
		u.draw()
		switch ev := u.s.PollEvent().(type) {
		case nil:
			return
		case *tcell.EventResize:
			u.s.Sync()
		case *tcell.EventKey:
			u.handleKey(ev)
		}
	}
}

// waitKey blocks until a key is pressed, redrawing the screen with
// overlay on top on refresh and resize events.
func (u *UI) waitKey(overlay func()) *tcell.EventKey {
	for {
		u.render()
		overlay()
		u.s.Show()
		switch ev := u.s.PollEvent().(type) {
		case nil:
			u.quit = true
			return tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)
		case *tcell.EventResize:
			u.s.Sync()
		case *tcell.EventKey:
			return ev
		}
	}
}

func (u *UI) current() (pfq.Message, bool) {
	if u.cur < 0 || u.cur >= len(u.msgs) {
		return pfq.Message{}, false
	}
	return u.msgs[u.cur], true
}

// clamp keeps the cursor inside the list and visible.
func (u *UI) clamp(lay layout) {
	n := len(u.msgs)
	if u.cur >= n {
		u.cur = n - 1
	}
	if u.cur < 0 {
		u.cur = 0
	}
	if u.cur < u.first {
		u.first = u.cur
	}
	if u.cur >= u.first+lay.listRows {
		u.first = u.cur - lay.listRows + 1
	}
	u.first = max(min(u.first, n-lay.listRows), 0)
}

// Navigation (goto_msg and friends).

func (u *UI) gotoMsg(i int) {
	if i < 0 || i >= len(u.msgs) {
		return
	}
	u.cur = i
	u.bodyFrom = 0
	u.clamp(u.layout())
}

func (u *UI) pageStep() int { return max(u.layout().listRows-1, 1) }

func (u *UI) gotoNextPage() {
	if u.cur+u.pageStep() < len(u.msgs)-1 {
		u.gotoMsg(u.cur + u.pageStep())
	} else {
		u.gotoMsg(len(u.msgs) - 1)
	}
}

func (u *UI) gotoPrevPage() {
	if u.cur > u.pageStep() {
		u.gotoMsg(u.cur - u.pageStep())
	} else {
		u.gotoMsg(0)
	}
}

// queueKeys maps the number keys to queues.
var queueKeys = map[rune]int{
	'1': backend.QueueDeferred,
	'2': backend.QueueActive,
	'3': backend.QueueIncoming,
	'4': backend.QueueHold,
	'5': backend.QueueCorrupt,
}

func (u *UI) changeQueue(q int) {
	if q >= u.c.NumQueues() {
		return
	}
	if u.c.SetQueue(q) == nil {
		u.msgs = nil
		u.cur, u.first, u.mark = 0, 0, -1
	}
}

func (u *UI) msgAction(act backend.Action) {
	m, ok := u.current()
	if !ok {
		return
	}
	if u.st.AskConfirm && !u.confirm(act.String()) {
		return
	}
	// errors are logged by the library
	_ = u.c.Action(m.ID, act)
}

func (u *UI) toggle(fn func(*pfq.Status)) { u.c.UpdateStatus(fn) }

func (u *UI) handleKey(ev *tcell.EventKey) {
	switch ev.Key() {
	case tcell.KeyEnd:
		u.gotoMsg(len(u.msgs) - 1)
	case tcell.KeyHome:
		u.gotoMsg(0)
	case tcell.KeyUp:
		u.gotoMsg(u.cur - 1)
	case tcell.KeyDown:
		u.gotoMsg(u.cur + 1)
	case tcell.KeyPgUp:
		u.gotoPrevPage()
	case tcell.KeyPgDn:
		u.gotoNextPage()
	case tcell.KeyDelete:
		u.msgAction(backend.ActionDelete)
	case tcell.KeyEnter, tcell.KeyLF:
		u.showBody()
	case tcell.KeyCtrlL:
		u.s.Sync()
	case tcell.KeyCtrlC:
		u.quit = true
	case tcell.KeyRune:
		u.handleRune(ev.Rune())
	}
}

func (u *UI) handleRune(r rune) {
	if q, ok := queueKeys[r]; ok {
		u.changeQueue(q)
		return
	}
	switch r {
	case 'q', 'Q':
		u.quit = true
	case 'G':
		u.gotoMsg(len(u.msgs) - 1)
	case 'g':
		u.gotoMsg(0)
	case 'e':
		u.c.ToggleEnvelope()
	case 'd':
		u.msgAction(backend.ActionDelete)
	case 'h':
		u.msgAction(backend.ActionHold)
	case 'l':
		u.msgAction(backend.ActionRelease)
	case 'r':
		u.msgAction(backend.ActionRequeue)
	case 'a':
		u.c.TagAll()
	case 'u':
		u.c.TagNone()
	case 'm':
		if m, ok := u.current(); ok {
			u.mark = u.cur
			u.c.Tag(m.ID)
		}
	case ' ', 't':
		m, ok := u.current()
		if !ok {
			break
		}
		if u.mark == -1 {
			u.c.ToggleTag(m.ID)
		} else {
			u.c.TagRange(u.mark, u.cur)
		}
		u.mark = -1
		u.gotoMsg(u.cur + 1)
	case 'T':
		if u.askSearch() {
			u.c.SearchAndTag()
		}
	case '/':
		if u.askSearch() {
			u.gotoMsg(u.c.SearchFirst())
		}
	case 'n':
		u.gotoMsg(u.c.SearchNext())
	case 'p':
		u.gotoMsg(u.c.SearchPrev())
	case ':':
		u.toggle(func(s *pfq.Status) { s.AutoWorkTagged = !s.AutoWorkTagged })
	case ';':
		u.toggle(func(s *pfq.Status) { s.WorkTagged = !s.WorkTagged })
	case 'c':
		u.toggle(func(s *pfq.Status) { s.AskConfirm = !s.AskConfirm })
	case '-':
		u.toggle(func(s *pfq.Status) { s.DoScan = !s.DoScan })
	case '+':
		canColor := u.s.Colors() >= 8
		u.toggle(func(s *pfq.Status) { s.UseColors = canColor && !s.UseColors })
	case 'b':
		u.showBodyWin = !u.showBodyWin
		if m, ok := u.current(); ok && u.showBodyWin {
			u.bodyID = m.ID
		}
	case 'B':
		u.showBodyAlways = !u.showBodyAlways
	case '>':
		if _, h := u.s.Size(); u.bodyHeight < h-10 {
			u.bodyHeight++
		}
	case '<':
		if u.bodyHeight > 5 {
			u.bodyHeight--
		}
	case ',':
		if u.showBodyWin {
			if u.bodyFrom > 0 {
				u.bodyFrom--
			}
			u.showBodyInWin()
		}
	case '.':
		if u.showBodyWin {
			u.bodyFrom++
			u.showBodyInWin()
		}
	case 's':
		u.bodyPopup()
	case 'S':
		if f, desc, ok := u.askSort(); ok {
			u.c.SetSort(f, desc)
		}
	case '?':
		u.help()
	}
}

// showBodyInWin shows the current message in the body window.
func (u *UI) showBodyInWin() {
	if m, ok := u.current(); ok && u.bodyID != m.ID {
		u.bodyID = m.ID
		u.bodyFrom = 0
	}
}

// showBody is the ENTER key: refresh the body window if visible,
// otherwise open the body viewer.
func (u *UI) showBody() {
	if !u.showBodyWin {
		u.bodyPopup()
		return
	}
	if m, ok := u.current(); ok {
		u.bodyID = m.ID
		u.bodyLines(m.ID, true)
	}
}

func (u *UI) askSearch() bool {
	expr, ok := u.prompt("Insert POSIX regular expression to search for:")
	if !ok {
		return false
	}
	return u.c.SetSearch(expr) == nil
}
