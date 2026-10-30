package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
)

const (
	headRows = 2 // header window (hwnd)
	msgRows  = 7 // message details window incl. border (mwndb)
	col1     = 20
)

// layout mirrors win_resize(): header, message list, message details and
// the optional body window, top to bottom.
type layout struct {
	w, h     int
	listRows int
	msgY     int
	bodyY    int
	col2     int
}

func (u *UI) layout() layout {
	w, h := u.s.Size()
	bh := 0
	if u.showBodyWin {
		bh = u.bodyHeight
	}
	rows := max(h-headRows-msgRows-bh, 1)
	return layout{
		w: w, h: h,
		listRows: rows,
		msgY:     headRows + rows,
		bodyY:    headRows + rows + msgRows,
		col2:     col1 + (w-col1)/2 - 2,
	}
}

// Styles (init_pair in the C version).

func (u *UI) styleHead() tcell.Style {
	if u.colors {
		return tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorNavy)
	}
	return tcell.StyleDefault
}

func (u *UI) styleCursor() tcell.Style {
	if u.colors {
		return tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorPurple)
	}
	return tcell.StyleDefault.Reverse(true)
}

func (u *UI) styleTag() tcell.Style {
	if u.colors {
		return tcell.StyleDefault.Foreground(tcell.ColorOlive).Background(tcell.ColorBlack).Bold(true)
	}
	return tcell.StyleDefault.Bold(true)
}

func (u *UI) styleDialog() tcell.Style {
	if u.colors {
		return tcell.StyleDefault.Foreground(tcell.ColorWhite).Background(tcell.ColorMaroon)
	}
	return tcell.StyleDefault
}

// put draws s at x,y using at most maxw cells (maxw < 0: up to the right
// screen edge). Control characters are shown as '?'.
func (u *UI) put(x, y int, s string, st tcell.Style, maxw int) {
	w, _ := u.s.Size()
	limit := w
	if maxw >= 0 && x+maxw < limit {
		limit = x + maxw
	}
	for _, r := range s {
		switch {
		case r == '\t':
			r = ' '
		case r < ' ' || r == 0x7f || (r >= 0x80 && r < 0xa0):
			r = '?'
		}
		rw := runewidth.RuneWidth(r)
		if rw == 0 {
			continue
		}
		if x+rw > limit {
			return
		}
		u.s.SetContent(x, y, r, nil, st)
		x += rw
	}
}

func (u *UI) fill(x, y, w, h int, st tcell.Style) {
	for j := y; j < y+h; j++ {
		for i := x; i < x+w; i++ {
			u.s.SetContent(i, j, ' ', nil, st)
		}
	}
}

// box draws a border like wborder(wnd, 0, ...).
func (u *UI) box(x, y, w, h int, st tcell.Style) {
	if w < 2 || h < 2 {
		return
	}
	for i := x + 1; i < x+w-1; i++ {
		u.s.SetContent(i, y, tcell.RuneHLine, nil, st)
		u.s.SetContent(i, y+h-1, tcell.RuneHLine, nil, st)
	}
	for j := y + 1; j < y+h-1; j++ {
		u.s.SetContent(x, j, tcell.RuneVLine, nil, st)
		u.s.SetContent(x+w-1, j, tcell.RuneVLine, nil, st)
	}
	u.s.SetContent(x, y, tcell.RuneULCorner, nil, st)
	u.s.SetContent(x+w-1, y, tcell.RuneURCorner, nil, st)
	u.s.SetContent(x, y+h-1, tcell.RuneLLCorner, nil, st)
	u.s.SetContent(x+w-1, y+h-1, tcell.RuneLRCorner, nil, st)
}

// frame draws a bordered window with a bold title (wnd_clear).
func (u *UI) frame(x, y, w, h int, title string, border, inner tcell.Style) {
	u.fill(x, y, w, h, inner)
	u.box(x, y, w, h, border)
	u.put(x+2, y, title, border.Bold(true), w-4)
}

// refreshData loads what is needed for the visible rows and takes a new
// snapshot of the queue.
func (u *UI) refreshData() {
	u.st = u.c.Status()
	lay := u.layout()
	u.msgs = u.c.Snapshot()
	u.clamp(lay)

	fetched := false
	for i := u.first; i < u.first+lay.listRows && i < len(u.msgs); i++ {
		if u.c.RetrHeaders(u.msgs[i].ID) {
			fetched = true
		}
	}
	if m, ok := u.current(); ok {
		u.c.RetrStatus(m.ID)
		fetched = true
	}
	if fetched {
		u.msgs = u.c.Snapshot()
		u.clamp(lay)
	}
	if u.showBodyWin && u.showBodyAlways {
		if m, ok := u.current(); ok {
			u.bodyID = m.ID
		}
	}
}

// render draws the main screen without showing it.
func (u *UI) render() {
	u.refreshData()
	lay := u.layout()
	u.s.Clear()
	u.drawHeader(lay)
	u.drawList(lay)
	u.drawMessage(lay)
	if u.showBodyWin {
		u.drawBodyWin(lay)
	}
}

func (u *UI) draw() {
	u.render()
	u.s.Show()
}

// drawHeader mirrors win_header().
func (u *UI) drawHeader(lay layout) {
	hs := u.styleHead().Bold(true)
	u.fill(0, 0, lay.w, headRows, u.styleHead())

	n := len(u.msgs)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	tagged := 0
	for _, m := range u.msgs {
		if m.Tagged {
			tagged++
		}
	}
	sortName := u.st.SortField.String()
	if u.st.SortField != 0 && u.st.SortDesc {
		sortName += " (desc)"
	}
	text := fmt.Sprintf("Queue: '%s', %d message%s, %d tagged, %s",
		u.c.QueueName(u.st.CurQueue), n, plural, tagged, sortName)
	u.put(1, 0, text, hs, lay.w-9)

	flags := []struct {
		ch string
		on bool
	}{
		{"A", u.st.AutoWorkTagged},
		{"T", u.st.WorkTagged},
		{"C", u.st.AskConfirm},
		{"S", u.st.DoScan},
		{"B", u.showBodyAlways},
	}
	for i, f := range flags {
		u.put(lay.w-7+i, 0, f.ch, hs.Reverse(f.on), 1)
	}

	u.put(1, 1, "ID", hs, -1)
	u.put(col1, 1, "From", hs, -1)
	u.put(lay.col2, 1, "To", hs, -1)
}

// drawList mirrors queue_show() and show_cursor().
func (u *UI) drawList(lay layout) {
	for r := 0; r < lay.listRows; r++ {
		i := u.first + r
		if i >= len(u.msgs) {
			break
		}
		m := u.msgs[i]
		y := headRows + r
		st := tcell.StyleDefault
		if m.Tagged {
			st = u.styleTag()
		}
		if i == u.cur {
			st = u.styleCursor().Bold(m.Tagged)
		}
		u.fill(0, y, lay.w, 1, st)
		u.put(1, y, m.ID, st, col1-2)
		u.put(col1, y, m.From, st, lay.col2-2-col1)
		u.put(lay.col2, y, m.To, st, lay.w-2-lay.col2)
	}
}

// drawMessage mirrors msg_show_headers().
func (u *UI) drawMessage(lay layout) {
	title := "Message (from headers)"
	if u.st.UseEnvelope {
		title = "Message (from envelope)"
	}
	u.frame(0, lay.msgY, lay.w, msgRows, title, u.styleHead(), tcell.StyleDefault)

	x, y, vw := 1, lay.msgY+1, lay.w-2-8
	m, ok := u.current()
	if !ok {
		u.put(x, y, "Queue is empty", tcell.StyleDefault, lay.w-2)
		return
	}
	rows := []struct{ label, val string }{
		{"Message:", m.ID},
		{"From..:", m.From},
		{"To....:", m.To},
		{"Subj..:", m.Subject},
		{"Status:", m.Stat},
	}
	for i, r := range rows {
		u.put(x, y+i, r.label, tcell.StyleDefault, -1)
		off := 8
		if i == 0 {
			off = 9
		}
		u.put(x+off, y+i, r.val, tcell.StyleDefault, vw)
	}
}

// drawBodyWin draws the body window below the message details.
func (u *UI) drawBodyWin(lay layout) {
	title := "Message body"
	m, ok := u.c.Message(u.bodyID)
	if ok {
		title = "Body of " + m.Path
	}
	u.frame(0, lay.bodyY, lay.w, u.bodyHeight, title, u.styleHead(), tcell.StyleDefault)
	if !ok {
		return
	}
	lines := u.bodyLines(u.bodyID, false)
	u.bodyFrom = min(u.bodyFrom, max(len(lines)-1, 0))
	for r := 0; r < u.bodyHeight-2; r++ {
		j := u.bodyFrom + r
		if j >= len(lines) {
			break
		}
		u.put(3, lay.bodyY+1+r, lines[j], tcell.StyleDefault, lay.w-4)
	}
}

// bodyLines returns the body of id split into lines, fetching it if it
// is not cached or force is set.
func (u *UI) bodyLines(id string, force bool) []string {
	if !force && u.body.id == id {
		return u.body.lines
	}
	u.body.id = id
	raw, err := u.c.RetrBody(id)
	if err != nil && len(raw) == 0 {
		u.body.lines = []string{"*Error*: " + err.Error()}
		return u.body.lines
	}
	u.body.lines = splitBody(string(raw))
	return u.body.lines
}

// splitBody splits text into lines, expanding tabs like curses does.
func splitBody(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = strings.TrimSuffix(l, "\r")
		if strings.Contains(l, "\t") {
			var b strings.Builder
			col := 0
			for _, r := range l {
				if r == '\t' {
					n := 8 - col%8
					b.WriteString(strings.Repeat(" ", n))
					col += n
					continue
				}
				b.WriteRune(r)
				col += runewidth.RuneWidth(r)
			}
			l = b.String()
		}
		lines[i] = l
	}
	return lines
}
