package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"

	"pfqueue/internal/backend"
	"pfqueue/internal/pfq"
)

// dialog draws a centered, bordered dialog window and returns its origin.
func (u *UI) dialog(h, w int) (int, int) {
	sw, sh := u.s.Size()
	x, y := max((sw-w)/2, 0), max((sh-h)/2, 0)
	st := u.styleDialog()
	u.fill(x, y, w, h, st)
	u.box(x, y, w, h, st)
	return x, y
}

// confirm asks for confirmation of an action (wnd_confirm).
func (u *UI) confirm(action string) bool {
	text := fmt.Sprintf("Do you really want to %s this message?", action)
	if u.c.ActsOnTagged() {
		text = fmt.Sprintf("Do you really want to %s tagged messages?", action)
	}
	ev := u.waitKey(func() {
		x, y := u.dialog(5, 50)
		u.put(x+2, y+2, text, u.styleDialog(), 46)
	})
	return ev.Key() == tcell.KeyRune && (ev.Rune() == 'y' || ev.Rune() == 'Y')
}

// askSort asks for the sort field (wnd_change_sort_field).
func (u *UI) askSort() (pfq.SortField, bool, bool) {
	lines := []string{
		"Choose sort field:",
		"",
		"0: unsorted",
		"1: from    (4 descendent)",
		"2: to      (5 descendent)",
		"3: subject (6 descendent)",
	}
	ev := u.waitKey(func() {
		x, y := u.dialog(8, 50)
		for i, l := range lines {
			u.put(x+2, y+1+i, l, u.styleDialog(), 46)
		}
	})
	if ev.Key() != tcell.KeyRune || ev.Rune() < '0' || ev.Rune() > '6' {
		return 0, false, false
	}
	r := int(ev.Rune() - '0')
	if r <= 3 {
		return pfq.SortField(r), false, true
	}
	return pfq.SortField(r - 3), true, true
}

// prompt reads a line of text (wnd_getregexp). ESC cancels.
func (u *UI) prompt(title string) (string, bool) {
	var buf []rune
	defer u.s.HideCursor()
	for {
		ev := u.waitKey(func() {
			x, y := u.dialog(5, 60)
			st := u.styleDialog()
			u.put(x+2, y+1, title, st, 56)
			// keep the end of long input visible
			text := string(buf)
			for runewidth.StringWidth(text) > 55 {
				_, n := utf8.DecodeRuneInString(text)
				text = text[n:]
			}
			u.put(x+2, y+3, text, st, 56)
			u.s.ShowCursor(x+2+runewidth.StringWidth(text), y+3)
		})
		switch ev.Key() {
		case tcell.KeyEnter, tcell.KeyLF:
			return string(buf), true
		case tcell.KeyEscape, tcell.KeyCtrlC:
			return "", false
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case tcell.KeyCtrlU:
			buf = buf[:0]
		case tcell.KeyRune:
			if len(buf) < regexpLen {
				buf = append(buf, ev.Rune())
			}
		}
		if u.quit {
			return "", false
		}
	}
}

// bodyPopup shows the body of the current message in a scrollable
// window (msg_show_body(1) / msg_cat_body with controls).
func (u *UI) bodyPopup() {
	m, ok := u.current()
	if !ok {
		return
	}
	lines := u.bodyLines(m.ID, true)
	fline := 0
	for {
		_, h := u.s.Size()
		pg := max(h-6, 1)
		ev := u.waitKey(func() {
			w, h := u.s.Size()
			u.frame(2, 2, w-4, h-4, "Body of "+m.Path, tcell.StyleDefault, tcell.StyleDefault)
			for i := 0; i < h-6 && fline+i < len(lines); i++ {
				u.put(5, 3+i, lines[fline+i], tcell.StyleDefault, w-8)
			}
		})
		last := max(len(lines)-pg, 0)
		switch ev.Key() {
		case tcell.KeyHome:
			fline = 0
		case tcell.KeyEnd:
			fline = last
		case tcell.KeyPgUp:
			fline = max(fline-pg, 0)
		case tcell.KeyPgDn:
			fline = min(fline+pg, last)
		case tcell.KeyUp:
			fline = max(fline-1, 0)
		case tcell.KeyDown:
			fline = min(fline+1, last)
		case tcell.KeyEscape:
			return
		case tcell.KeyRune:
			switch ev.Rune() {
			case 'g':
				fline = 0
			case 'G':
				fline = last
			case ' ':
				fline = min(fline+1, last)
			case 'q', 'Q':
				return
			}
		}
		if u.quit {
			return
		}
	}
}

const helpText1 = `  Messages and action keys:
  e) Toggle envelope reading for from/to
  d) Delete message
  h) Hold message
  r) Requeue message
  l) Release message in 'hold' queue
  m) Mark message as tag block start
  t) Tag/Untag message (up to mark if present)
  a) Tag all messages
  u) Untag all messages
  ;) Make actions work on tagged messages
  :) Toggle auto-work-on-tagged
  s) Show selected message
  /) Search in From: or To: headers
  n) Search next
  p) Search previous
  T) Search and tag messages
  c) Toggle confirmation request
  -) Suspend/resume queue scanning
  +) Toggle colors on/off
  ?) This help
  q) Quit`

const helpText2 = `  In the windows, you can use G/END, g/HOME, PgUP/DN keys
  S) Sort messages
  To change current queue:
  1) Deferred queue
  2) Active queue
  3) Incoming queue
  4) Hold queue
  5) Corrupt queue
  b) Show/hide body window
  B) Toggle automatic body show
  ENTER) Show body of current message
  >) Increase body window height
  <) Decrease body window height
  ,) Scroll body window up
  .) Scroll body window down`

func (u *UI) helpBackend() string {
	from := map[int]string{0: "none", 1: "/etc only", 2: "HOME only", 3: "/etc and HOME"}
	return fmt.Sprintf(`  pfqueue lib version: %s
  Backend API version: %d
  Config source: %s

  Backend ID: %s
  Backend version: %s
  Backend lib: built-in

  Frontend name: %s
  Frontend version: %s
  Frontend lib: built-in`,
		pfq.LibVersion, backend.APIVersion, from[u.c.Conf.ConfigFrom],
		u.c.BackendID(), u.c.BackendVersion(), Name, Version)
}

// help shows the help pages; q leaves, any other key turns the page.
func (u *UI) help() {
	pages := []string{helpText1, helpText2, u.helpBackend()}
	page := 0
	for {
		ev := u.waitKey(func() {
			sw, sh := u.s.Size()
			x, y := max((sw-60)/2, 0), max((sh-26)/2, 0)
			st := u.styleDialog()
			u.fill(x, y, 60, 25, st)
			for i, l := range strings.Split(pages[page], "\n") {
				u.put(x, y+1+i, l, st, 59)
			}
			u.box(x, y, 60, 25, st)
			bold := st.Bold(true)
			u.put(x+2, y, "pfqueue ver. "+u.info.Version, bold, -1)
			u.put(x+28, y, "Help", bold, -1)
			u.put(x+2, y+23, "Press q to exit help, any key for next page", bold, 56)
		})
		if u.quit || ev.Key() == tcell.KeyEscape ||
			(ev.Key() == tcell.KeyRune && (ev.Rune() == 'q' || ev.Rune() == 'Q')) {
			return
		}
		page = (page + 1) % len(pages)
	}
}
