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
)

// digDir walks base recursively (following symlinked directories, skipping
// dot entries) and collects regular files accepted by accept, up to max
// entries. It stops early when ctx is done and returns what it found.
func digDir(ctx context.Context, base string, max int, accept func(name string) (id string, ok bool)) []Entry {
	var out []Entry
	var dig func(dir string)
	dig = func(dir string) {
		if len(out) >= max || ctx.Err() != nil {
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
				dig(full)
			case mode.IsRegular():
				if id, ok := accept(name); ok {
					out = append(out, Entry{ID: id, Path: full})
				}
			}
		}
	}
	dig(base)
	return out
}

// command builds an exec.Cmd with stderr discarded (like "2>/dev/null").
func command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Stderr = nil
	return cmd
}

// firstLine runs a command and returns the first line of its output.
func firstLine(name string, args ...string) (string, error) {
	out, err := command(name, args...).Output()
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimRight(line, "\r"), nil
}

// readLines runs a command and calls fn for each output line until fn
// returns false. The process is killed if it is not fully consumed.
func readLines(fn func(line string) bool, name string, args ...string) error {
	cmd := command(name, args...)
	stdout, err := cmd.StdoutPipe()
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
	if stopped {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil
	}
	return cmd.Wait()
}

// readLimited runs a command and returns at most max bytes of its output,
// optionally skipping the first output line.
func readLimited(max int, skipFirstLine bool, name string, args ...string) ([]byte, error) {
	cmd := command(name, args...)
	stdout, err := cmd.StdoutPipe()
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
	buf, rerr := io.ReadAll(io.LimitReader(r, int64(max)))
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if rerr != nil {
		return buf, rerr
	}
	return buf, nil
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
}

func newHeaderParser(fromKey, toKey, nullSender string, skip int) *headerParser {
	return &headerParser{
		fromKey: fromKey, toKey: toKey, nullSender: nullSender, skip: skip,
		h: Headers{From: StrNotFound, To: StrNotFound},
	}
}

// feed processes one line and reports whether more input is wanted.
func (p *headerParser) feed(line string) bool {
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
