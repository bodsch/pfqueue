// Package backend contains the MTA specific queue access.
//
// It replaces the dlopen()ed libpfq_*.so backends of the C version; all
// backends are compiled in and selected by name.
package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// APIVersion mirrors PFQ_API_VERSION of the C backends.
const APIVersion = 4

// Caps describes what a backend is able to do.
type Caps int

const (
	CapDelete     Caps = 1 << iota // can delete messages
	CapHold                        // can hold (freeze) messages
	CapRequeue                     // can requeue messages
	CapEnvelope                    // can extract from/to from envelope
	CapMixedQueue                  // single queue, status kept elsewhere
	CapMsgLog                      // each message has its own log
)

// Action is an operation on a queued message.
type Action int

const (
	ActionHold Action = iota
	ActionDelete
	ActionRelease
	ActionRequeue
)

var actionNames = [...]string{"hold", "delete", "release", "requeue"}

func (a Action) String() string {
	if a < 0 || int(a) >= len(actionNames) {
		return "unknown"
	}
	return actionNames[a]
}

// Queue indices as used by postfix (and the socket protocol).
const (
	QueueDeferred = 0
	QueueHold     = 1
	QueueIncoming = 2
	QueueActive   = 3
	QueueCorrupt  = 4
)

const (
	// StrError is shown when a field cannot be retrieved.
	StrError = "*Error*"
	// StrNotFound is shown when a header is missing.
	StrNotFound = "*Not found*"
)

// ErrUnusable is returned by Setup when the MTA is not available.
var ErrUnusable = errors.New("backend unusable")

// ErrInvalidID is returned for a message id that is not a plausible queue
// id. Ids are passed as arguments to the MTA tools and over the socket
// protocol, so they must be checked first.
var ErrInvalidID = errors.New("invalid message id")

// maxIDLen is far above the longest postfix (long queue id) or exim id.
const maxIDLen = 64

// ValidID reports whether id may be passed to an MTA tool or a remote
// server. Postfix and exim ids consist of letters, digits and '-'. An id
// starting with '-' would be read as an option, "-" makes postsuper read
// ids from stdin and "ALL" makes it act on the whole queue; a newline
// would inject a second command into the socket protocol.
func ValidID(id string) bool {
	if id == "" || len(id) > maxIDLen || id[0] == '-' || id == "ALL" {
		return false
	}
	return strings.IndexFunc(id, func(c rune) bool { return !idChar(c) }) < 0
}

func idChar(c rune) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || c == '-'
}

// Entry is a message found while scanning a queue.
type Entry struct {
	ID   string
	Path string
}

// Headers holds the fields shown in the message list.
type Headers struct {
	From     string
	To       string
	Subject  string
	Path     string // optional, updates the message path if set
	Complete bool   // all fields were found; result may be cached
}

// Config is the backend configuration (struct pfb_conf_t).
type Config struct {
	CommandPath string // path to MTA executables
	ConfigPath  string // MTA configuration (dir or file)
	Host        string // remote host (socket backend)
	Port        int    // remote port (socket backend)
}

// Backend is the interface every MTA backend implements.
type Backend interface {
	ID() string
	Version() string
	Setup(Config) error
	Close() error

	QueueCount() int
	QueueName(q int) string
	SetQueue(q int) error
	Caps() Caps

	// Scan lists the messages in the current queue, at most max entries.
	// When ctx expires the entries found so far are returned.
	Scan(ctx context.Context, max int) ([]Entry, error)
	Headers(e Entry, envelope bool) (Headers, error)
	// Status returns the message status; cacheable tells whether the
	// value may be kept for the lifetime of the message.
	Status(e Entry) (status string, cacheable bool, err error)
	Body(e Entry, max int) ([]byte, error)
	Action(id string, a Action) error
}

// Names lists the backends in autodetect order.
var Names = []string{"exim", "postfix2"}

// New returns an unconfigured backend by name.
func New(name string) (Backend, error) {
	switch name {
	case "postfix2", "postfix":
		return newPostfix(), nil
	case "exim":
		return newExim(), nil
	case "socket":
		return newSocket(), nil
	}
	return nil, fmt.Errorf("unknown backend %q", name)
}
