package backend

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeScript writes an executable shell script into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// within fails the test if fn does not return in time, instead of letting
// a hang block the whole test binary.
func within(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not return within %v", d)
	}
}

func readLog(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"ABC123":                        true,
		"4Xyz5T0abcz9":                  true,
		"1abcDE-000001-AB":              true,
		"":                              false,
		"-d":                            false,
		"-":                             false,
		"ALL":                           false,
		"A B":                           false,
		"A\nQUIT":                       false,
		"../etc":                        false,
		"ID;rm":                         false,
		strings.Repeat("a", maxIDLen):   true,
		strings.Repeat("a", maxIDLen+1): false,
	} {
		if got := ValidID(id); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestReadLinesStopsEarly(t *testing.T) {
	// yes never ends on its own: the process must be killed
	var n int
	within(t, 10*time.Second, func() {
		err := readLines(func(string) bool { n++; return n < 3 }, "yes")
		if err != nil {
			t.Errorf("readLines: %v", err)
		}
	})
	if n != 3 {
		t.Fatalf("lines read = %d", n)
	}
}

func TestReadLinesLongLine(t *testing.T) {
	// a line longer than the scanner buffer followed by endless output:
	// before the fix the process blocked on a full pipe and Wait hung
	script := `head -c 2000000 /dev/zero | tr '\0' a; echo; yes`
	within(t, 10*time.Second, func() {
		err := readLines(func(string) bool { return true }, "sh", "-c", script)
		if !errors.Is(err, bufio.ErrTooLong) {
			t.Errorf("err = %v, want bufio.ErrTooLong", err)
		}
	})
}

func TestReadLimited(t *testing.T) {
	within(t, 10*time.Second, func() {
		out, err := readLimited(5, true, "sh", "-c", "echo skipped; yes body")
		if err != nil || string(out) != "body\n" {
			t.Errorf("readLimited = %q, %v", out, err)
		}
	})
}

func TestDigDirSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(dir, "loop")); err != nil {
		t.Fatal(err)
	}
	accept := func(name string) (string, bool) { return name, true }
	within(t, 10*time.Second, func() {
		if got := digDir(context.Background(), dir, 100, accept); len(got) != 0 {
			t.Errorf("entries = %v", got)
		}
	})
}

func TestDigDirCancelled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "A"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := digDir(ctx, dir, 100, func(n string) (string, bool) { return n, true }); len(got) != 0 {
		t.Fatalf("entries after cancel = %v", got)
	}
}

const fakePostcat = `for a; do id=$a; done
case $id in
NOSUBJ)
	echo '*** ENVELOPE RECORDS ***'
	echo 'sender: alice@example.com'
	echo '*** MESSAGE CONTENTS ***'
	echo 'From: alice@example.com'
	echo 'To: bob@example.org'
	echo ''
	echo 'Subject: this is body text'
	yes 'body'
	;;
*)
	echo '*** ENVELOPE RECORDS ***'
	echo 'sender: alice@example.com'
	echo 'recipient: bob@example.org'
	echo '*** MESSAGE CONTENTS ***'
	echo 'From: Alice <alice@example.com>'
	echo 'To: bob@example.org'
	echo 'Subject: hello'
	echo ''
	echo 'body line'
	;;
esac
`

// fakePostfix sets up a postfix backend whose tools are shell scripts.
// postsuper records its arguments in the returned log file.
func fakePostfix(t *testing.T) (*postfix, string, string) {
	t.Helper()
	bin, qdir := t.TempDir(), t.TempDir()
	logFile := filepath.Join(t.TempDir(), "postsuper.log")
	writeScript(t, bin, "postconf", `for a; do last=$a; done
case $last in
mail_version) echo 3.9.1 ;;
queue_directory) echo '`+qdir+`' ;;
esac
`)
	writeScript(t, bin, "postcat", fakePostcat)
	writeScript(t, bin, "postsuper", `echo "$@" >> '`+logFile+"'\n")

	for _, f := range []string{"deferred/A/ABC123", "deferred/B/DEF456", "hold/HLD789"} {
		p := filepath.Join(qdir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	p := newPostfix()
	if err := p.Setup(Config{CommandPath: bin, ConfigPath: "/etc/postfix-test"}); err != nil {
		t.Fatal(err)
	}
	return p, qdir, logFile
}

func TestPostfixBackend(t *testing.T) {
	p, qdir, logFile := fakePostfix(t)
	if p.queueDir != qdir || p.version != pfVersion22 {
		t.Fatalf("setup: dir %q version %d", p.queueDir, p.version)
	}

	entries, err := p.Scan(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.ID)
	}
	if !slices.Equal(got, []string{"ABC123", "DEF456"}) {
		t.Fatalf("deferred = %v", got)
	}

	h, err := p.Headers(entries[0], false)
	want := Headers{From: "Alice <alice@example.com>", To: "bob@example.org", Subject: "hello", Complete: true}
	if err != nil || h != want {
		t.Fatalf("headers = %+v, %v", h, err)
	}
	body, err := p.Body(entries[0], 1<<20)
	if err != nil || !strings.Contains(string(body), "body line") {
		t.Fatalf("body = %q, %v", body, err)
	}

	for _, a := range []Action{ActionDelete, ActionHold, ActionRelease, ActionRequeue} {
		if err := p.Action("ABC123", a); err != nil {
			t.Fatal(err)
		}
	}
	wantLog := []string{
		"-c /etc/postfix-test -d ABC123",
		"-c /etc/postfix-test -h ABC123",
		"-c /etc/postfix-test -H ABC123",
		"-c /etc/postfix-test -r ABC123",
	}
	if got := readLog(t, logFile); !slices.Equal(got, wantLog) {
		t.Fatalf("postsuper calls = %q", got)
	}

	if err := p.SetQueue(QueueHold); err != nil {
		t.Fatal(err)
	}
	if entries, _ := p.Scan(context.Background(), 100); len(entries) != 1 || entries[0].ID != "HLD789" {
		t.Fatalf("hold = %v", entries)
	}
	if err := p.SetQueue(len(postfixQueues)); err == nil {
		t.Fatal("SetQueue out of range accepted")
	}
}

func TestPostfixRejectsDangerousIDs(t *testing.T) {
	p, _, logFile := fakePostfix(t)
	for _, id := range []string{"ALL", "-", "-d", "A B"} {
		if err := p.Action(id, ActionDelete); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Action(%q) = %v", id, err)
		}
		if _, err := p.Headers(Entry{ID: id}, false); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Headers(%q) = %v", id, err)
		}
		if _, err := p.Body(Entry{ID: id}, 10); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Body(%q) = %v", id, err)
		}
	}
	if _, err := os.Stat(logFile); !os.IsNotExist(err) {
		t.Fatal("postsuper was run with an invalid id")
	}
}

func TestPostfixHeadersStopAtBody(t *testing.T) {
	p, _, _ := fakePostfix(t)
	var h Headers
	var err error
	// postcat writes an endless body: reading must stop at its start
	within(t, 10*time.Second, func() { h, err = p.Headers(Entry{ID: "NOSUBJ"}, false) })
	if err != nil {
		t.Fatal(err)
	}
	if h.Subject != "" || h.Complete || h.From != "alice@example.com" {
		t.Fatalf("headers = %+v", h)
	}
}

func TestPostfixUnusable(t *testing.T) {
	bin := t.TempDir()
	writeScript(t, bin, "postconf", "exit 1\n")
	if err := newPostfix().Setup(Config{CommandPath: bin}); !errors.Is(err, ErrUnusable) {
		t.Fatalf("Setup = %v", err)
	}
}

func TestPostfixDeferPath(t *testing.T) {
	p := &postfix{queueDir: "/var/spool/deferred-mail"}
	for in, want := range map[string]string{
		"/var/spool/deferred-mail/deferred/A/ABC": "/var/spool/deferred-mail/defer/A/ABC",
		"/var/spool/deferred-mail/hold/ABC":       "",
		"/elsewhere/deferred/A/ABC":               "",
	} {
		if got := p.deferPath(in); got != want {
			t.Errorf("deferPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEximBackend(t *testing.T) {
	bin, spool := t.TempDir(), t.TempDir()
	logFile := filepath.Join(t.TempDir(), "exim.log")
	// exim 3 is missing, so Setup must fall back to exim4
	writeScript(t, bin, "exim4", `echo "$@" >> '`+logFile+`'
case "$*" in
*-bP*) echo 'spool_directory = `+spool+`' ;;
*-Mvh*1abcDE-000001-AB*)
	echo '1abcDE-000001-AB-H'
	echo '-frozen 1700000000'
	echo ''
	echo '030F From: alice@example.com'
	echo '025T To: bob@example.org'
	echo '015  Subject: hi'
	;;
*-Mvb*) echo '1abcDE-000001-AB-D'; echo 'the body' ;;
esac
`)
	for _, f := range []string{"input/a/1abcDE-000001-AB-H", "input/a/1abcDE-000001-AB-D", "msglog/x-H"} {
		p := filepath.Join(spool, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	x := newExim()
	if err := x.Setup(Config{CommandPath: bin, ConfigPath: "/etc/exim.conf"}); err != nil {
		t.Fatal(err)
	}
	if x.spoolDir != spool || x.QueueCount() != 1 || x.QueueName(0) != "input" {
		t.Fatalf("setup = %+v", x)
	}
	entries, err := x.Scan(context.Background(), 100)
	if err != nil || len(entries) != 1 || entries[0].ID != "1abcDE-000001-AB" {
		t.Fatalf("scan = %v, %v", entries, err)
	}
	e := entries[0]
	h, err := x.Headers(e, false)
	want := Headers{From: "alice@example.com", To: "bob@example.org", Subject: "hi", Complete: true}
	if err != nil || h != want {
		t.Fatalf("headers = %+v, %v", h, err)
	}
	if st, _, err := x.Status(e); err != nil || st != "Frozen" {
		t.Fatalf("status = %q, %v", st, err)
	}
	if body, err := x.Body(e, 100); err != nil || string(body) != "the body\n" {
		t.Fatalf("body = %q, %v", body, err)
	}
	if err := x.Action(e.ID, ActionHold); err != nil {
		t.Fatal(err)
	}
	if err := x.Action("-Mrm", ActionDelete); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("invalid id accepted: %v", err)
	}
	log := readLog(t, logFile)
	if last := log[len(log)-1]; last != "-C /etc/exim.conf -Mf 1abcDE-000001-AB" {
		t.Fatalf("last exim call = %q", last)
	}
}

func TestNewAndActionNames(t *testing.T) {
	for _, name := range []string{"postfix", "postfix2", "exim", "socket"} {
		if _, err := New(name); err != nil {
			t.Errorf("New(%q): %v", name, err)
		}
	}
	if _, err := New("sendmail"); err == nil {
		t.Error("unknown backend accepted")
	}
	if ActionRequeue.String() != "requeue" || Action(42).String() != "unknown" {
		t.Error("action names")
	}
}

// shortCmdTimeout lowers cmdTimeout for one test.
func shortCmdTimeout(t *testing.T) {
	old := cmdTimeout
	cmdTimeout = 200 * time.Millisecond
	t.Cleanup(func() { cmdTimeout = old })
}

// TestCommandTimeoutFires checks the promise that every run of an MTA tool
// is limited. postcat hanging on a locked queue file would otherwise stall
// the scanner, which holds the scan lock, and with it every action.
func TestCommandTimeoutFires(t *testing.T) {
	shortCmdTimeout(t)
	// exec: the hanging process is the one started, as postcat would be
	cases := map[string]func() error{
		"run": func() error { return run("sleep", "30") },
		"firstLine": func() error {
			_, err := firstLine("sleep", "30")
			return err
		},
		"readLines": func() error {
			return readLines(func(string) bool { return true }, "sh", "-c", "echo x; exec sleep 30")
		},
	}
	for name, fn := range cases {
		within(t, 5*time.Second, func() {
			if err := fn(); err == nil {
				t.Errorf("%s: hanging command reported success", name)
			}
		})
	}
}

// TestCommandTimeoutWithChildHoldingStdout covers a tool that leaves a
// child behind with its stdout open (a wrapper script around postcat, say).
// Killing the tool alone does not end the read, so the scanner would still
// hang until the child exits.
func TestCommandTimeoutWithChildHoldingStdout(t *testing.T) {
	shortCmdTimeout(t)
	script := "echo x; sleep 30; true" // sh stays, sleep inherits stdout
	within(t, 5*time.Second, func() {
		if err := readLines(func(string) bool { return true }, "sh", "-c", script); err == nil {
			t.Error("readLines: no error")
		}
	})
	within(t, 5*time.Second, func() {
		_, _ = readLimited(1<<20, false, "sh", "-c", script)
	})
}
