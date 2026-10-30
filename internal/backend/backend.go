// Package backend contains the MTA specific queue access.
//
// It replaces the dlopen()ed libpfq_*.so backends of the C version; all
// backends are compiled in and selected by name.
package backend

import (
	"context"
	"errors"
	"fmt"
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
