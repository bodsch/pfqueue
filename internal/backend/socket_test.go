package backend

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer answers socket protocol requests with respond, which returns
// the raw reply including the line end.
func fakeServer(t *testing.T, respond func(req string) string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := conn.Write([]byte(respond(strings.TrimSpace(line)))); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestSocketBodyLengthIsCapped(t *testing.T) {
	body := strings.Repeat("x", 5000)
	addr := fakeServer(t, func(req string) string {
		switch {
		case req == "NUMQ":
			return "RESULT: 00001\n"
		case strings.HasPrefix(req, "QNAME"):
			return "RESULT: deferred\n"
		case strings.HasPrefix(req, "BODY"):
			return fmt.Sprintf("RESULT: %06d,%s\n", len(body), body)
		case strings.HasPrefix(req, "MSGFROM"):
			return "RESULT: after-body\n"
		}
		return "ERR: NOCMD\n"
	})
	s := newSocket()
	if err := s.Setup(Config{Host: addr}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	got, err := s.Body(Entry{ID: "A1"}, 100)
	if err != nil || len(got) != 100 {
		t.Fatalf("body = %d bytes, %v", len(got), err)
	}
	// the rest of the body must have been consumed, not left in the stream
	if v := s.field("MSGFROM", "A1"); v != "after-body" {
		t.Fatalf("next reply = %q", v)
	}
}

func TestSocketRejectsImplausibleQueueCount(t *testing.T) {
	for _, n := range []string{"00000", "99999", "-1", "x"} {
		addr := fakeServer(t, func(string) string { return "RESULT: " + n + "\n" })
		if err := newSocket().Setup(Config{Host: addr}); !errors.Is(err, ErrUnusable) {
			t.Errorf("NUMQ %s: Setup = %v", n, err)
		}
	}
	if err := newSocket().Setup(Config{}); !errors.Is(err, ErrUnusable) {
		t.Errorf("no host: %v", err)
	}
}

func TestSocketRejectsInjectedIDs(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	sent := func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(reqs) }
	addr := fakeServer(t, func(req string) string {
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		if req == "NUMQ" {
			return "RESULT: 00001\n"
		}
		return "RESULT: q\n"
	})
	s := newSocket()
	if err := s.Setup(Config{Host: addr}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	n := len(sent())
	if err := s.Action("A1\nMSGDEL B2", ActionDelete); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("Action = %v", err)
	}
	if v := s.field("MSGFROM", "A1 B2"); v != StrError {
		t.Fatalf("field = %q", v)
	}
	if got := sent(); len(got) != n {
		t.Fatalf("requests sent: %q", got[n:])
	}
}

func TestParseReply(t *testing.T) {
	if v, err := parseReply("RESULT: 00042"); err != nil || v != "00042" {
		t.Errorf("result = %q, %v", v, err)
	}
	if _, err := parseReply("ERROR-: NOMSG"); err == nil {
		t.Error("error reply accepted")
	}
	if v, err := parseReply("RES"); err != nil || v != "" {
		t.Errorf("short reply = %q, %v", v, err)
	}
}

// TestSocketTimeoutFires checks "gives up on a request after 30 seconds":
// a remote pfqueue that accepts but never answers (stopped with SIGSTOP,
// stuck on a hung postcat) must not freeze the local screen for good.
func TestSocketTimeoutFires(t *testing.T) {
	old := socketTimeout
	socketTimeout = 200 * time.Millisecond
	t.Cleanup(func() { socketTimeout = old })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = conn.Close() }) // read nothing, answer nothing
		}
	}()

	within(t, 5*time.Second, func() {
		if err := newSocket().Setup(Config{Host: ln.Addr().String()}); !errors.Is(err, ErrUnusable) {
			t.Errorf("Setup against a silent server = %v", err)
		}
	})
}
