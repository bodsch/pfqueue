package backend

import (
	"context"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

const (
	pfVersion20 = 1
	pfVersion21 = 2
	pfVersion22 = 3
)

const (
	headerFrom     = "From: "
	headerTo       = "To: "
	headerSubject  = "Subject: "
	envelopeFrom   = "sender: "
	envelopeTo     = "recipient: "
	nullEnvSender  = "Null envelope sender"
	contentsMark   = "*** MESSAGE CONTENTS"
	deferNoReason  = "Deferred, no reason"
	deferReasonKey = "reason="
)

// postfix implements the backend for postfix 2.x and later.
type postfix struct {
	version   int
	queueDir  string
	configDir string
	postconf  string
	postsuper string
	postcat   string
	curQueue  atomic.Int32
}

func newPostfix() *postfix { return &postfix{} }

var postfixQueues = []string{"deferred", "hold", "incoming", "active", "corrupt"}

func (p *postfix) ID() string      { return "postfix 2.x" }
func (p *postfix) Version() string { return "1.3" }

func (p *postfix) Caps() Caps {
	return CapHold | CapDelete | CapRequeue | CapEnvelope
}

// cfgArgs prepends "-c <config>" when a config directory is set.
func (p *postfix) cfgArgs(args ...string) []string {
	if p.configDir != "" {
		return append([]string{"-c", p.configDir}, args...)
	}
	return args
}

func (p *postfix) Setup(c Config) error {
	p.configDir = c.ConfigPath
	p.postconf, p.postsuper, p.postcat = "postconf", "postsuper", "postcat"
	if c.CommandPath != "" {
		p.postconf = filepath.Join(c.CommandPath, "postconf")
		p.postsuper = filepath.Join(c.CommandPath, "postsuper")
		p.postcat = filepath.Join(c.CommandPath, "postcat")
	}
	p.curQueue.Store(0)

	p.version = pfVersion22
	if v, err := firstLine(p.postconf, p.cfgArgs("-h", "mail_version")...); err == nil {
		switch {
		case strings.HasPrefix(v, "2.0"):
			p.version = pfVersion20
		case strings.HasPrefix(v, "2.1"):
			p.version = pfVersion21
		}
	}

	dir, err := firstLine(p.postconf, p.cfgArgs("-h", "queue_directory")...)
	if err != nil || dir == "" {
		return ErrUnusable
	}
	p.queueDir = dir
	return nil
}

func (p *postfix) Close() error { return nil }

func (p *postfix) QueueCount() int { return len(postfixQueues) }

func (p *postfix) QueueName(q int) string {
	if q < 0 || q >= len(postfixQueues) {
		return "noname"
	}
	return postfixQueues[q]
}

func (p *postfix) SetQueue(q int) error {
	if q < 0 || q >= p.QueueCount() {
		return ErrUnusable
	}
	p.curQueue.Store(int32(q))
	return nil
}

func (p *postfix) Scan(ctx context.Context, max int) ([]Entry, error) {
	dir := filepath.Join(p.queueDir, p.QueueName(int(p.curQueue.Load())))
	return digDir(ctx, dir, max, func(name string) (string, bool) { return name, true }), nil
}

// catArgs returns the postcat arguments for a message.
func (p *postfix) catArgs(e Entry) []string { return p.cfgArgs("-q", e.ID) }

func (p *postfix) Headers(e Entry, envelope bool) (Headers, error) {
	if !ValidID(e.ID) {
		return Headers{From: StrError, To: StrError}, ErrInvalidID
	}
	hp := newPostfixParser(envelope)
	if err := readLines(hp.feed, p.postcat, p.catArgs(e)...); err != nil && !hp.finished() {
		return Headers{From: StrError, To: StrError}, err
	}
	return hp.result(), nil
}

func newPostfixParser(envelope bool) *headerParser {
	hp := newHeaderParser(headerFrom, headerTo, nullEnvSender, 0)
	if envelope {
		hp = newHeaderParser(envelopeFrom, envelopeTo, nullEnvSender, 0)
	}
	hp.contentsMark = contentsMark
	return hp
}

// decodeHeader decodes RFC 2047 encoded words; undecodable input is kept.
func decodeHeader(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	dec := new(mime.WordDecoder)
	if out, err := dec.DecodeHeader(s); err == nil {
		return out
	}
	return s
}

func (p *postfix) Status(e Entry) (string, bool, error) {
	switch int(p.curQueue.Load()) {
	case QueueDeferred:
		data, err := os.ReadFile(p.deferPath(e.Path))
		if err != nil {
			return deferNoReason, true, nil
		}
		if p.version > pfVersion20 {
			if r, ok := lookFor(data, deferReasonKey); ok {
				return r, true, nil
			}
			return deferNoReason, true, nil
		}
		line, _, _ := strings.Cut(string(data), "\n")
		return line, true, nil
	case QueueActive:
		return "Active", true, nil
	case QueueHold:
		return "Held", true, nil
	case QueueIncoming:
		return "Incoming", true, nil
	case QueueCorrupt:
		return "Corrupt", true, nil
	}
	return "", true, nil
}

// deferPath maps a message in the deferred queue to its defer log, which
// lives under "defer" instead of "deferred". Only the queue directory
// component is replaced, never a "deferred" elsewhere in the path.
func (p *postfix) deferPath(path string) string {
	rel, err := filepath.Rel(filepath.Join(p.queueDir, "deferred"), path)
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	return filepath.Join(p.queueDir, "defer", rel)
}

func (p *postfix) Body(e Entry, max int) ([]byte, error) {
	if !ValidID(e.ID) {
		return nil, ErrInvalidID
	}
	return readLimited(max, false, p.postcat, p.catArgs(e)...)
}

func (p *postfix) Action(id string, a Action) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	var o string
	switch a {
	case ActionDelete:
		o = "-d"
	case ActionHold:
		o = "-h"
	case ActionRelease:
		o = "-H"
	case ActionRequeue:
		o = "-r"
	default:
		return ErrUnusable
	}
	return run(p.postsuper, p.cfgArgs(o, id)...)
}
