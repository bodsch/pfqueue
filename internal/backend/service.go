package backend

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// cmdTimeout bounds every run of an MTA tool, so a hanging postcat or
// exim cannot freeze the scanner or the screen. A variable for tests.
var cmdTimeout = 30 * time.Second

const (
	// maxDigDepth bounds the queue directory recursion. Postfix hashes at
	// most a few levels deep; the limit stops a symlink loop.
	maxDigDepth = 8
)

// digDir walks base recursively (following symlinked directories, skipping
// dot entries) and collects regular files accepted by accept, up to max
// entries. It stops early when ctx is done and returns what it found.
func digDir(ctx context.Context, base string, max int, accept func(name string) (id string, ok bool)) []Entry {
	var out []Entry
	var dig func(dir string, depth int)
	dig = func(dir string, depth int) {
		if len(out) >= max || depth > maxDigDepth || ctx.Err() != nil {
			return
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, d := range ents {
			if len(out) >= max || ctx.Err() != nil {
				return
			}
			name := d.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(dir, name)
			mode := d.Type()
			if mode&os.ModeSymlink != 0 {
				fi, err := os.Stat(full)
				if err != nil || !fi.IsDir() {
					continue
				}
				mode = os.ModeDir
			}
			switch {
			case mode.IsDir():
				dig(full, depth+1)
			case mode.IsRegular():
				if id, ok := accept(name); ok {
					out = append(out, Entry{ID: id, Path: full})
				}
			}
		}
	}
	dig(base, 0)
	return out
}

// command builds an exec.Cmd that is killed after cmdTimeout. Stderr is
// discarded (like "2>/dev/null"). The caller must call the returned
// cancel function once the command has finished.
func command(name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	cmd := exec.CommandContext(ctx, name, args...)
	// do not wait for grandchildren holding stdout open after a kill
	cmd.WaitDelay = time.Second
	return cmd, cancel
}

// run runs a command and waits for it to finish.
func run(name string, args ...string) error {
	cmd, cancel := command(name, args...)
	defer cancel()
	return cmd.Run()
}

// stdoutPipe returns the command's stdout. On timeout the pipe is closed
// as well as the process killed: a child that inherited stdout would
// otherwise keep the read, and with it the caller, blocked after the kill.
func stdoutPipe(cmd *exec.Cmd) (io.ReadCloser, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Cancel = func() error {
		_ = stdout.Close() // unblocks the reader
		return cmd.Process.Kill()
	}
	return stdout, nil
}

// firstLine runs a command and returns the first line of its output.
func firstLine(name string, args ...string) (string, error) {
	cmd, cancel := command(name, args...)
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimRight(line, "\r"), nil
}

// readLines runs a command and calls fn for each output line until fn
// returns false. The process is killed if it is not fully consumed.
func readLines(fn func(line string) bool, name string, args ...string) error {
	cmd, cancel := command(name, args...)
	defer cancel()
	stdout, err := stdoutPipe(cmd)
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	stopped := false
	for sc.Scan() {
		if !fn(sc.Text()) {
			stopped = true
			break
		}
	}
	// Kill on an early stop and also on a scanner error (a line longer
	// than the buffer): the process would otherwise block on a full pipe
	// and Wait would never return.
	if serr := sc.Err(); stopped || serr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return serr
	}
	return cmd.Wait()
}

// readLimited runs a command and returns at most max bytes of its output,
// optionally skipping the first output line.
func readLimited(max int, skipFirstLine bool, name string, args ...string) ([]byte, error) {
	cmd, cancel := command(name, args...)
	defer cancel()
	stdout, err := stdoutPipe(cmd)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	r := bufio.NewReader(stdout)
	if skipFirstLine {
		_, _ = r.ReadString('\n')
	}
	buf, err := io.ReadAll(io.LimitReader(r, int64(max)))
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return buf, err
}

// lookFor returns the remainder of the first line starting with prefix.
func lookFor(data []byte, prefix string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), prefix); ok {
			return rest, true
		}
	}
	return "", false
}

// headerParser extracts From/To/Subject from MTA output lines. Only the
// first occurrence of each field is used; skip is a fixed per-line prefix
// length (exim -Mvh prints "NNNF " before each header).
type headerParser struct {
	fromKey, toKey, nullSender string
	skip                       int
	h                          Headers
	gotFrom, gotTo, gotSubj    bool

	// contentsMark, if set, starts the message contents; the first empty
	// line after it ends the headers, so a "Subject:" in the body of a
	// message without one is not taken for the header.
	contentsMark string
	inContents   bool
	ended        bool
}

func newHeaderParser(fromKey, toKey, nullSender string, skip int) *headerParser {
	return &headerParser{
		fromKey: fromKey, toKey: toKey, nullSender: nullSender, skip: skip,
		h: Headers{From: StrNotFound, To: StrNotFound},
	}
}

// feed processes one line and reports whether more input is wanted.
func (p *headerParser) feed(line string) bool {
	if p.contentsMark != "" {
		if strings.HasPrefix(line, p.contentsMark) {
			p.inContents = true
			return true
		}
		if p.inContents && line == "" {
			p.ended = true
			return false
		}
	}
	if len(line) < p.skip {
		return true
	}
	line = line[p.skip:]
	if !p.gotFrom {
		if v, ok := cutField(line, p.fromKey); ok {
			p.h.From = decodeHeader(v)
			if p.h.From == "" {
				p.h.From = p.nullSender
			}
			p.gotFrom = true
		}
	}
	if !p.gotTo {
		if v, ok := cutField(line, p.toKey); ok {
			p.h.To = decodeHeader(v)
			p.gotTo = true
		}
	}
	if !p.gotSubj {
		if v, ok := cutField(line, headerSubject); ok {
			p.h.Subject = decodeHeader(v)
			p.gotSubj = true
		}
	}
	return !p.done()
}

func (p *headerParser) done() bool { return p.gotFrom && p.gotTo && p.gotSubj }

// finished reports whether the parser needs no more input: every field
// was found or the end of the headers was reached.
func (p *headerParser) finished() bool { return p.done() || p.ended }

func (p *headerParser) result() Headers {
	h := p.h
	h.Complete = p.done() && h.From != "" && h.To != ""
	return h
}

// cutField returns the value of a "Key: value" line. A bare "Key:" line
// (empty value, trailing blank stripped) matches as well.
func cutField(line, key string) (string, bool) {
	if v, ok := strings.CutPrefix(line, key); ok {
		return v, true
	}
	return "", line == strings.TrimRight(key, " ")
}
