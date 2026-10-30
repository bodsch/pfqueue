package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"pfqueue/internal/backend/fakebe"
	"pfqueue/internal/pfq"
)

// serve starts a server on a fake backend and returns its address and a
// function that stops it and returns the result of Serve.
func serve(t *testing.T, msgs ...fakebe.Msg) (string, func() error) {
	t.Helper()
	conf := pfq.DefaultConfig()
	conf.ScanDelay = 3600
	lib := pfq.New(conf, pfq.DefaultStatus(), nil)
	if err := lib.StartWith(fakebe.New(msgs)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lib.Close)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- New(lib, log.New(io.Discard, "", 0)).Serve(ctx, ln) }()
	stop := sync.OnceValue(func() error {
		cancel()
		select {
		case err := <-serveErr:
			return err
		case <-time.After(10 * time.Second):
			return errors.New("Serve did not return")
		}
	})
	t.Cleanup(func() { _ = stop() })
	return ln.Addr().String(), stop
}

type client struct {
	conn net.Conn
	r    *bufio.Reader
}

func dial(t *testing.T, addr string) *client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return &client{conn: conn, r: bufio.NewReader(conn)}
}

func (c *client) ask(t *testing.T, req string) string {
	t.Helper()
	if _, err := io.WriteString(c.conn, req+"\n"); err != nil {
		t.Fatal(err)
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		t.Fatalf("%s: %v", req, err)
	}
	return strings.TrimSuffix(line, "\n")
}

func TestProtocolReplies(t *testing.T) {
	addr, _ := serve(t,
		fakebe.Msg{ID: "A1", From: "alice", Subject: "evil\r\nRESULT: forged"},
		fakebe.Msg{ID: "B2"},
	)
	c := dial(t, addr)
	for req, want := range map[string]string{
		"NUMMSG":       "RESULT: 00002",
		"NUMQ":         "RESULT: 00001",
		"QNAME 0":      "RESULT: q0",
		"MSGID 1":      "RESULT: B2",
		"MSGID 7":      "ERROR-: NOMSG",
		"LSTIDS":       "RESULT: A1,B2",
		"MSGFROM A1":   "RESULT: alice",
		"MSGFROM nope": "ERROR-: NOMSG",
		"MSGSTAT A1":   "RESULT: Fake",
		"BODY nope":    "ERROR-: NOMSG",
		"SETQ 5":       "ERROR-: no such queue 5",
		"FOO":          "ERR: NOCMD",
		// a decoded header must not break the line protocol
		"MSGSUBJ A1": "RESULT: evil RESULT: forged",
	} {
		if got := c.ask(t, req); got != want {
			t.Errorf("%s = %q, want %q", req, got, want)
		}
	}
	if got := c.ask(t, "LASTC"); !strings.HasPrefix(got, "RESULT: 1") {
		t.Errorf("LASTC = %q", got)
	}
	// QUIT closes the connection
	if _, err := io.WriteString(c.conn, "QUIT\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.r.ReadString('\n'); !errors.Is(err, io.EOF) {
		t.Errorf("after QUIT: %v", err)
	}
}

func TestOverlongLineDisconnects(t *testing.T) {
	addr, _ := serve(t, fakebe.Msg{ID: "A1"})
	c := dial(t, addr)
	// the server reads at most maxLine bytes and then hangs up; the write
	// may fail once the connection is gone
	_, _ = io.WriteString(c.conn, strings.Repeat("A", 4*maxLine))
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("overlong line answered")
	}
	// the server keeps serving others
	if got := dial(t, addr).ask(t, "NUMMSG"); got != "RESULT: 00001" {
		t.Fatalf("NUMMSG = %q", got)
	}
}

func TestClientLimit(t *testing.T) {
	addr, _ := serve(t)
	for range MaxClients {
		dial(t, addr).ask(t, "NUMQ") // registered once answered
	}
	extra := dial(t, addr)
	_, _ = io.WriteString(extra.conn, "NUMQ\n")
	if _, err := extra.r.ReadString('\n'); err == nil {
		t.Fatal("client beyond MaxClients served")
	}
}

func TestShutdownClosesClients(t *testing.T) {
	addr, stop := serve(t)
	c := dial(t, addr)
	c.ask(t, "NUMQ")
	// an idle client must not keep Serve from returning
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("connection still open after shutdown")
	}
}

func TestReadLine(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("NUMQ\r\nrest"), 16)
	if l, err := readLine(r); err != nil || l != "NUMQ" {
		t.Fatalf("line = %q, %v", l, err)
	}
	if _, err := readLine(r); !errors.Is(err, io.EOF) {
		t.Fatalf("unterminated line: %v", err)
	}
	r = bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 64)+"\n"), 16)
	if _, err := readLine(r); !errors.Is(err, errLineTooLong) {
		t.Fatalf("long line: %v", err)
	}
}

// TestStalledReaderIsDropped checks the write timeout. A client that sends
// requests but never reads its replies fills the socket buffers; without
// the timeout its handler blocks for good and keeps one of the MaxClients
// slots, so 32 such clients lock everybody out.
func TestStalledReaderIsDropped(t *testing.T) {
	old := writeTimeout
	writeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { writeTimeout = old })

	body := strings.Repeat("x", pfq.BodyMax)
	addr, _ := serve(t, fakebe.Msg{ID: "A1", Body: body})
	c := dial(t, addr)
	if tc, ok := c.conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096) // fill the buffers sooner
	}
	// ask for far more than the socket buffers hold, read nothing
	go func() {
		req := strings.Repeat("BODY A1\n", 1000) // about 20 MB of replies
		_, _ = io.WriteString(c.conn, req)
	}()
	time.Sleep(time.Second)

	// The server must have hung up: reading drains what was sent, then
	// ends with EOF, or with a reset because our requests were unread.
	// Only running into the read deadline means the handler is still there.
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, c.r)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("connection still open after the write timeout")
	}
}
