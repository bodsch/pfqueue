package backend

import (
	"context"
	"path/filepath"
	"strings"
)

// eximHeaderSkip is the "NNNF " prefix exim -Mvh prints before headers.
const eximHeaderSkip = 5

type exim struct {
	cmd      string
	conf     string
	spoolDir string
}

func newExim() *exim { return &exim{} }

func (x *exim) ID() string      { return "exim" }
func (x *exim) Version() string { return "1.3" }

func (x *exim) Caps() Caps {
	return CapHold | CapDelete | CapRequeue | CapMixedQueue | CapMsgLog
}

// args prepends "-C <config>" when a config file is set.
func (x *exim) args(args ...string) []string {
	if x.conf != "" {
		return append([]string{"-C", x.conf}, args...)
	}
	return args
}

func (x *exim) Setup(c Config) error {
	x.conf = c.ConfigPath
	// exim 3 is called "exim", exim 4 often "exim4"
	for _, name := range []string{"exim", "exim4"} {
		x.cmd = name
		if c.CommandPath != "" {
			x.cmd = filepath.Join(c.CommandPath, name)
		}
		line, err := firstLine(x.cmd, x.args("-bP", "spool_directory")...)
		if err != nil {
			continue
		}
		if dir := parseEximOption(line); dir != "" {
			x.spoolDir = dir
			return nil
		}
	}
	return ErrUnusable
}

// parseEximOption extracts the value of "name = value".
func parseEximOption(line string) string {
	_, v, ok := strings.Cut(line, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func (x *exim) Close() error { return nil }

func (x *exim) QueueCount() int { return 1 }

func (x *exim) QueueName(q int) string {
	if q == 0 {
		return "input"
	}
	return "noname"
}

func (x *exim) SetQueue(q int) error {
	if q != 0 {
		return ErrUnusable
	}
	return nil
}

func (x *exim) Scan(ctx context.Context, max int) ([]Entry, error) {
	dir := filepath.Join(x.spoolDir, "input")
	return digDir(ctx, dir, max, func(name string) (string, bool) {
		if id, ok := strings.CutSuffix(name, "-H"); ok && id != "" {
			return id, true
		}
		return "", false
	}), nil
}

func (x *exim) Headers(e Entry, _ bool) (Headers, error) {
	if !ValidID(e.ID) {
		return Headers{From: StrError, To: StrError}, ErrInvalidID
	}
	hp := newHeaderParser(headerFrom, headerTo, "Null sender", eximHeaderSkip)
	if err := readLines(hp.feed, x.cmd, x.args("-Mvh", e.ID)...); err != nil && !hp.finished() {
		return Headers{From: StrError, To: StrError}, err
	}
	return hp.result(), nil
}

func (x *exim) Status(e Entry) (string, bool, error) {
	if !ValidID(e.ID) {
		return StrError, false, ErrInvalidID
	}
	status := "Active"
	err := readLines(func(line string) bool {
		if strings.HasPrefix(line, "-frozen") {
			status = "Frozen"
			return false
		}
		return true
	}, x.cmd, x.args("-Mvh", e.ID)...)
	if err != nil {
		return "Cannot run exim", false, err
	}
	return status, false, nil
}

func (x *exim) Body(e Entry, max int) ([]byte, error) {
	if !ValidID(e.ID) {
		return nil, ErrInvalidID
	}
	// the first line of -Mvb output is the spool file name
	return readLimited(max, true, x.cmd, x.args("-Mvb", e.ID)...)
}

func (x *exim) Action(id string, a Action) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	var o string
	switch a {
	case ActionDelete:
		o = "-Mrm"
	case ActionHold:
		o = "-Mf"
	case ActionRelease:
		o = "-Mt"
	case ActionRequeue:
		o = "-M"
	default:
		return ErrUnusable
	}
	return run(x.cmd, x.args(o, id)...)
}
