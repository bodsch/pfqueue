// Package pfq is the queue library shared by all frontends (libpfq in the
// C version). It owns the backend, keeps the message list of the current
// queue up to date in a background goroutine and implements tagging,
// searching, sorting and message actions.
package pfq

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"pfqueue/internal/backend"
)

const (
	// LibVersion mirrors PFQL_VERSION.
	LibVersion = "1.5"
	// BodyMax is the maximum number of body bytes retrieved (CAT_BUF_SIZE).
	BodyMax = 20 * 1024
	// headerRetry is how long incomplete headers are kept before the
	// backend is asked again.
	headerRetry = 10 * time.Second
	// volatileStatusTTL is how long a non cacheable status is reused.
	volatileStatusTTL = time.Second
)

// ErrNoMessage is returned for a message id that is not in the queue.
var ErrNoMessage = errors.New("message does not exist")

// SortField selects the list ordering.
type SortField int

const (
	SortUnsorted SortField = iota
	SortFrom
	SortTo
	SortSubject
)

var sortNames = [...]string{"unsorted", "sorted by from", "sorted by to", "sorted by subject"}

func (f SortField) String() string {
	if f < 0 || int(f) >= len(sortNames) {
		return ""
	}
	return sortNames[f]
}

// Search modes, may be combined.
const (
	SearchFrom = 1 << iota
	SearchTo
	SearchSubject
	SearchAll = SearchFrom | SearchTo | SearchSubject
)

// Config is the static configuration (struct pfql_conf_t).
type Config struct {
	InitialQueue  int
	BackendsPath  string // kept for compatibility, backends are built in
	BackendName   string
	BackendConfig string
	BackendProgs  string
	MaxMessages   int
	ScanLimit     int // max seconds for a single scan, 0 = unlimited
	ScanDelay     int // seconds between scans
	Refresh       int // seconds between screen refreshes
	RemoteHost    string
	RemotePort    int
	ListenAddress string // socket frontend: host or host:port, "" = all interfaces
	ConfigFrom    int    // 1 = /etc, 2 = $HOME, 3 = both

	FrontendName  string
	FrontendsPath string // kept for compatibility, frontends are built in
}

// DefaultConfig returns the defaults of pfql_init().
func DefaultConfig() Config {
	return Config{
		BackendName:  "autodetect",
		FrontendName: "ncurses",
		MaxMessages:  200,
		ScanDelay:    1,
		Refresh:      1,
		RemotePort:   20000,
	}
}

// Status is the runtime state (struct pfql_status_t).
type Status struct {
	AutoWorkTagged bool // work on tagged messages automatically, if any
	WorkTagged     bool // work on tagged messages
	AskConfirm     bool
	DoScan         bool
	UseEnvelope    bool
	UseColors      bool
	CurQueue       int
	SortField      SortField
	SortDesc       bool
}

// DefaultStatus returns the defaults of pfql_init().
func DefaultStatus() Status {
	return Status{AskConfirm: true, DoScan: true, UseColors: true}
}

// Message is a queued message as shown by the frontends.
type Message struct {
	ID      string
	From    string
	To      string
	Subject string
	Path    string
	Stat    string
	Tagged  bool

	hAt       time.Time // headers retrieved at
	hComplete bool
	sAt       time.Time // status retrieved at
	sCache    bool      // status valid for the message lifetime
}

func (m *Message) needsHeaders() bool {
	return m.hAt.IsZero() || (!m.hComplete && time.Since(m.hAt) > headerRetry)
}

func (m *Message) needsStatus() bool {
	return m.sAt.IsZero() || (!m.sCache && time.Since(m.sAt) > volatileStatusTTL)
}

func (m *Message) entry() backend.Entry { return backend.Entry{ID: m.ID, Path: m.Path} }

type scanReq struct {
	force bool
	done  chan struct{}
}

// Context is the library handle (struct pfql_context_t).
type Context struct {
	Conf   Config
	Log    *log.Logger
	be     backend.Backend
	scanMu sync.Mutex // held while scanning or acting (dig_suspend)

	mu          sync.Mutex
	status      Status
	queue       []*Message
	byID        map[string]*Message // index into queue, first message per id
	lastChanged time.Time

	search     *regexp.Regexp
	searchMode int
	searchPos  int

	rescan chan scanReq
	quit   chan struct{}
	done   chan struct{}
}

// New creates a library context.
func New(conf Config, st Status, logger *log.Logger) *Context {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Context{Conf: conf, Log: logger, status: st}
}

// Start selects and sets up the backend, starts the scanner and switches
// to the initial queue (pfql_backend_start).
func (c *Context) Start() error {
	bc := backend.Config{
		CommandPath: c.Conf.BackendProgs,
		ConfigPath:  c.Conf.BackendConfig,
		Host:        c.Conf.RemoteHost,
		Port:        c.Conf.RemotePort,
	}
	if c.Conf.BackendName == "autodetect" || c.Conf.BackendName == "" {
		for _, name := range backend.Names {
			c.Log.Printf("looking for backend '%s'", name)
			be, _ := backend.New(name)
			if err := be.Setup(bc); err != nil {
				c.Log.Printf("backend %s can't be used: %v", name, err)
				continue
			}
			c.be = be
			c.Conf.BackendName = name
			break
		}
		if c.be == nil {
			return errors.New("cannot autodetect suitable backend, try -b option")
		}
	} else {
		be, err := backend.New(c.Conf.BackendName)
		if err != nil {
			return err
		}
		if err := be.Setup(bc); err != nil {
			return fmt.Errorf("%s backend failed to setup: %w", c.Conf.BackendName, err)
		}
		c.be = be
	}
	return c.StartWith(c.be)
}

// StartWith starts the library with an already set up backend.
func (c *Context) StartWith(be backend.Backend) error {
	c.be = be
	c.Log.Printf("backend %s is OK", c.be.ID())
	if be.Caps()&backend.CapEnvelope == 0 {
		// -e / use_envelope=yes cannot apply; the frontend would otherwise
		// label header addresses as envelope ones
		c.UpdateStatus(func(s *Status) { s.UseEnvelope = false })
	}

	c.rescan = make(chan scanReq)
	c.quit = make(chan struct{})
	c.done = make(chan struct{})
	go c.scanLoop()

	q := c.Conf.InitialQueue
	if q >= c.be.QueueCount() {
		q = 0
	}
	return c.SetQueue(q)
}

// Close stops the scanner and the backend (pfql_context_destroy).
func (c *Context) Close() {
	if c.quit != nil {
		close(c.quit)
		<-c.done
	}
	if c.be != nil {
		if err := c.be.Close(); err != nil {
			c.Log.Printf("closing backend: %v", err)
		}
	}
}

// Backend information.

func (c *Context) BackendID() string      { return c.be.ID() }
func (c *Context) BackendVersion() string { return c.be.Version() }
func (c *Context) BackendCaps() backend.Caps {
	return c.be.Caps()
}
func (c *Context) NumQueues() int         { return c.be.QueueCount() }
func (c *Context) QueueName(q int) string { return c.be.QueueName(q) }
func (c *Context) BackendAPIVersion() int { return backend.APIVersion }

// Status returns a copy of the runtime status.
func (c *Context) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// UpdateStatus modifies the runtime status.
func (c *Context) UpdateStatus(fn func(*Status)) {
	c.mu.Lock()
	fn(&c.status)
	c.mu.Unlock()
}

// SetSort changes the sort order and resorts the queue soon.
func (c *Context) SetSort(f SortField, desc bool) {
	c.UpdateStatus(func(s *Status) { s.SortField, s.SortDesc = f, desc })
	c.Rescan()
}

// LastChanged returns the last time the message list changed.
func (c *Context) LastChanged() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastChanged
}

// Len returns the number of messages in the queue.
func (c *Context) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// NumTagged returns the number of tagged messages.
func (c *Context) NumTagged() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.numTaggedLocked()
}

func (c *Context) numTaggedLocked() int {
	n := 0
	for _, m := range c.queue {
		if m.Tagged {
			n++
		}
	}
	return n
}

// Snapshot returns a copy of the message list.
func (c *Context) Snapshot() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Message, len(c.queue))
	for i, m := range c.queue {
		out[i] = *m
	}
	return out
}

// MessageAt returns the message at position i.
func (c *Context) MessageAt(i int) (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i < 0 || i >= len(c.queue) {
		return Message{}, false
	}
	return *c.queue[i], true
}

// Message returns a message by id.
func (c *Context) Message(id string) (Message, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m := c.findLocked(id); m != nil {
		return *m, true
	}
	return Message{}, false
}

func (c *Context) findLocked(id string) *Message { return c.byID[id] }

// RetrHeaders loads From/To/Subject of a message unless cached. It
// reports whether the backend was asked.
func (c *Context) RetrHeaders(id string) bool {
	c.mu.Lock()
	m := c.findLocked(id)
	if m == nil || !m.needsHeaders() {
		c.mu.Unlock()
		return false
	}
	e, env := m.entry(), c.status.UseEnvelope
	c.mu.Unlock()

	h, err := c.be.Headers(e, env)
	if err != nil {
		c.Log.Printf("cannot read headers of %s: %v", id, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if m = c.findLocked(id); m != nil && env == c.status.UseEnvelope {
		m.From, m.To, m.Subject = h.From, h.To, h.Subject
		if h.Path != "" {
			m.Path = h.Path
		}
		m.hAt, m.hComplete = time.Now(), h.Complete && err == nil
	}
	return true
}

// RetrStatus loads the status line of a message unless cached.
func (c *Context) RetrStatus(id string) {
	c.mu.Lock()
	m := c.findLocked(id)
	if m == nil || !m.needsStatus() {
		c.mu.Unlock()
		return
	}
	e := m.entry()
	c.mu.Unlock()

	st, cacheable, err := c.be.Status(e)
	if err != nil {
		c.Log.Printf("cannot read status of %s: %v", id, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if m = c.findLocked(id); m != nil {
		m.Stat, m.sAt, m.sCache = st, time.Now(), cacheable
	}
}

// RetrBody returns up to BodyMax bytes of the message.
func (c *Context) RetrBody(id string) ([]byte, error) {
	m, ok := c.Message(id)
	if !ok {
		return nil, ErrNoMessage
	}
	return c.be.Body(m.entry(), BodyMax)
}

// Tagging.

func (c *Context) setTag(id string, fn func(bool) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m := c.findLocked(id); m != nil {
		m.Tagged = fn(m.Tagged)
	}
}

func (c *Context) Tag(id string)       { c.setTag(id, func(bool) bool { return true }) }
func (c *Context) Untag(id string)     { c.setTag(id, func(bool) bool { return false }) }
func (c *Context) ToggleTag(id string) { c.setTag(id, func(t bool) bool { return !t }) }

// TagRange tags the messages between positions a and b (inclusive).
func (c *Context) TagRange(a, b int) {
	if a > b {
		a, b = b, a
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := max(a, 0); i <= b && i < len(c.queue); i++ {
		c.queue[i].Tagged = true
	}
}

// TagAll tags every message.
func (c *Context) TagAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.queue {
		m.Tagged = true
	}
}

// TagNone untags every message and leaves work-on-tagged mode.
func (c *Context) TagNone() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tagNoneLocked()
}

func (c *Context) tagNoneLocked() {
	for _, m := range c.queue {
		m.Tagged = false
	}
	c.status.WorkTagged = false
}

// ActsOnTagged reports whether the next action works on tagged messages.
func (c *Context) ActsOnTagged() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status.WorkTagged || (c.status.AutoWorkTagged && c.numTaggedLocked() > 0)
}

// Action performs act on the tagged messages (if working on tagged) or on
// the message id (pfql_msg_action).
func (c *Context) Action(id string, act backend.Action) error {
	c.scanMu.Lock()
	c.mu.Lock()
	var ids []string
	if c.status.WorkTagged || (c.status.AutoWorkTagged && c.numTaggedLocked() > 0) {
		for _, m := range c.queue {
			if m.Tagged {
				ids = append(ids, m.ID)
			}
		}
		c.tagNoneLocked()
	} else if c.findLocked(id) != nil {
		ids = []string{id}
	}
	c.mu.Unlock()

	var errs []error
	for _, mid := range ids {
		if err := c.be.Action(mid, act); err != nil {
			c.Log.Printf("%s of %s failed: %v", act, mid, err)
			errs = append(errs, fmt.Errorf("%s %s: %w", act, mid, err))
		}
	}
	c.scanMu.Unlock()
	c.Rescan()
	return errors.Join(errs...)
}

// ToggleEnvelope switches between envelope and header from/to, if the
// backend supports it.
func (c *Context) ToggleEnvelope() {
	if c.be.Caps()&backend.CapEnvelope == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.UseEnvelope = !c.status.UseEnvelope
	for _, m := range c.queue {
		m.hAt = time.Time{}
	}
}

// SetQueue switches to queue q and waits until it has been scanned.
func (c *Context) SetQueue(q int) error {
	if q < 0 || q >= c.be.QueueCount() {
		return fmt.Errorf("no such queue %d", q)
	}
	c.scanMu.Lock()
	c.mu.Lock()
	c.status.CurQueue = q
	c.status.WorkTagged = false
	c.setQueueLocked(nil)
	c.mu.Unlock()
	err := c.be.SetQueue(q)
	c.scanMu.Unlock()
	if err != nil {
		return err
	}
	req := scanReq{force: true, done: make(chan struct{})}
	select {
	case c.rescan <- req:
		<-req.done
	case <-c.done:
	}
	return nil
}

// Rescan asks the scanner to run soon without waiting for it.
func (c *Context) Rescan() {
	if c.rescan == nil {
		return
	}
	select {
	case c.rescan <- scanReq{}:
	default:
	}
}

func (c *Context) scanLoop() {
	defer close(c.done)
	for {
		delay := time.Duration(max(c.Conf.ScanDelay, 1)) * time.Second
		var req scanReq
		select {
		case <-c.quit:
			return
		case req = <-c.rescan:
		case <-time.After(delay):
		}
		c.scanOnce(req.force)
		if req.done != nil {
			close(req.done)
		}
	}
}

// scanOnce refreshes the message list (if scanning is enabled or forced)
// and sorts it.
func (c *Context) scanOnce(force bool) {
	c.scanMu.Lock()
	defer c.scanMu.Unlock()

	st := c.Status()
	if st.DoScan || force {
		ctx := context.Background()
		if c.Conf.ScanLimit > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, time.Duration(c.Conf.ScanLimit)*time.Second)
			defer cancel()
		}
		entries, err := c.be.Scan(ctx, c.Conf.MaxMessages)
		if err != nil {
			c.Log.Printf("scan failed: %v", err)
		} else {
			c.merge(st.CurQueue, entries)
		}
	}

	if st.SortField != SortUnsorted {
		for _, m := range c.Snapshot() {
			if m.needsHeaders() {
				c.RetrHeaders(m.ID)
			}
		}
		c.mu.Lock()
		q := append([]*Message(nil), c.queue...)
		sortMessages(q, c.status.SortField, c.status.SortDesc)
		c.replaceLocked(q)
		c.mu.Unlock()
	}
}

// merge replaces the message list with entries, keeping cached data and
// tags of messages that are still present.
func (c *Context) merge(queue int, entries []backend.Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.CurQueue != queue {
		// queue switched while scanning
		return
	}
	old := make(map[string]*Message, len(c.queue))
	for _, m := range c.queue {
		old[m.ID] = m
	}
	q := make([]*Message, 0, len(entries))
	for _, e := range entries {
		m, ok := old[e.ID]
		if !ok || (e.Path != "" && m.Path != e.Path) {
			m = &Message{ID: e.ID, Path: e.Path}
		}
		q = append(q, m)
	}
	// keep the visible order stable while a sort order is active
	if c.status.SortField != SortUnsorted {
		sortMessages(q, c.status.SortField, c.status.SortDesc)
	}
	c.replaceLocked(q)
}

// replaceLocked installs q as the message list, recording a change.
func (c *Context) replaceLocked(q []*Message) {
	changed := len(q) != len(c.queue)
	for i := 0; !changed && i < len(q); i++ {
		changed = q[i] != c.queue[i]
	}
	if changed {
		c.setQueueLocked(q)
	}
}

// setQueueLocked installs q and rebuilds the id index. With duplicate ids
// (a symlinked queue directory) the first message wins, as a linear
// search would find it.
func (c *Context) setQueueLocked(q []*Message) {
	c.queue = q
	c.byID = make(map[string]*Message, len(q))
	for _, m := range q {
		if _, dup := c.byID[m.ID]; !dup {
			c.byID[m.ID] = m
		}
	}
	c.lastChanged = time.Now()
}

func sortMessages(q []*Message, f SortField, desc bool) {
	key := func(m *Message) string {
		switch f {
		case SortFrom:
			return m.From
		case SortTo:
			return m.To
		case SortSubject:
			return m.Subject
		}
		return ""
	}
	slices.SortStableFunc(q, func(a, b *Message) int {
		if desc {
			return cmp.Compare(key(b), key(a))
		}
		return cmp.Compare(key(a), key(b))
	})
}

// Searching.

// ParseSearch splits an optional f:, t:, e: or s: prefix off expr.
func ParseSearch(expr string) (string, int) {
	for _, p := range searchPrefixes {
		if rest, ok := strings.CutPrefix(expr, p.prefix); ok {
			return rest, p.mode
		}
	}
	return expr, SearchAll
}

var searchPrefixes = []struct {
	prefix string
	mode   int
}{
	{"f:", SearchFrom},
	{"t:", SearchTo},
	{"e:", SearchFrom | SearchTo},
	{"s:", SearchSubject},
}

// SetSearch compiles the search expression (with optional field prefix).
func (c *Context) SetSearch(expr string) error {
	re, mode := ParseSearch(expr)
	rx, err := regexp.Compile(re)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.search, c.searchMode, c.searchPos = rx, mode, -1
	c.mu.Unlock()
	return nil
}

func (c *Context) matches(i int) bool {
	m, ok := c.MessageAt(i)
	if !ok {
		return false
	}
	if c.RetrHeaders(m.ID) {
		m, _ = c.MessageAt(i)
	}
	c.mu.Lock()
	rx, mode := c.search, c.searchMode
	c.mu.Unlock()
	return (mode&SearchFrom != 0 && rx.MatchString(m.From)) ||
		(mode&SearchTo != 0 && rx.MatchString(m.To)) ||
		(mode&SearchSubject != 0 && rx.MatchString(m.Subject))
}

// searchFrom looks for a match starting at the stored position moved by
// one in dir (+1 / -1). It returns the position or -1.
func (c *Context) searchFrom(dir int) int {
	c.mu.Lock()
	if c.search == nil {
		c.mu.Unlock()
		return -1
	}
	pos := c.searchPos + dir
	c.mu.Unlock()

	for i := pos; i >= 0 && i < c.Len(); i += dir {
		if c.matches(i) {
			c.mu.Lock()
			c.searchPos = i
			c.mu.Unlock()
			return i
		}
	}
	return -1
}

// SearchFirst returns the first matching message position, or -1.
func (c *Context) SearchFirst() int {
	c.mu.Lock()
	c.searchPos = -1
	c.mu.Unlock()
	return c.searchFrom(1)
}

// SearchNext returns the next matching message position, or -1.
func (c *Context) SearchNext() int { return c.searchFrom(1) }

// SearchPrev returns the previous matching message position, or -1.
func (c *Context) SearchPrev() int { return c.searchFrom(-1) }

// SearchAndTag tags every matching message and returns their count.
func (c *Context) SearchAndTag() int {
	n := 0
	for i := c.SearchFirst(); i != -1; i = c.SearchNext() {
		if m, ok := c.MessageAt(i); ok {
			c.Tag(m.ID)
			n++
		}
	}
	return n
}

// MapQueueArg maps the user queue numbers of -q / default_queue
// (1 deferred, 2 active, 3 incoming, 4 hold) to queue indices.
func MapQueueArg(n int) int {
	switch n {
	case 2:
		return backend.QueueActive
	case 3:
		return backend.QueueIncoming
	case 4:
		return backend.QueueHold
	}
	return backend.QueueDeferred
}
