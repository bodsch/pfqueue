package server

import (
	"context"
	"log"
	"net"
	"slices"
	"testing"

	"pfqueue/internal/backend"
	"pfqueue/internal/backend/fakebe"
	"pfqueue/internal/pfq"
)

// TestRoundTrip runs the socket frontend on a fake backend and talks to
// it through the socket backend.
func TestRoundTrip(t *testing.T) {
	fake := fakebe.New(
		[]fakebe.Msg{
			{ID: "A1", From: "alice", To: "bob", Subject: "hi", Body: "line1\nline2\n"},
			{ID: "B2", From: "carol", To: "dave", Subject: "yo", Body: "x"},
		},
		[]fakebe.Msg{{ID: "H1"}},
	)
	conf := pfq.DefaultConfig()
	conf.ScanDelay = 3600
	lib := pfq.New(conf, pfq.DefaultStatus(), nil)
	if err := lib.StartWith(fake); err != nil {
		t.Fatal(err)
	}
	defer lib.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- New(lib, log.New(discard{}, "", 0)).Serve(ctx, ln) }()
	// runs after the deferred cancel()
	t.Cleanup(func() {
		if err := <-serveErr; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	be, _ := backend.New("socket")
	if err := be.Setup(backend.Config{Host: ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := be.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	if n := be.QueueCount(); n != 2 || be.QueueName(1) != "q1" {
		t.Fatalf("queues = %d %q", n, be.QueueName(1))
	}
	entries, err := be.Scan(ctx, 100)
	if err != nil || len(entries) != 2 || entries[1].ID != "B2" {
		t.Fatalf("scan = %v %v", entries, err)
	}
	h, _ := be.Headers(entries[0], false)
	if h.From != "alice" || h.To != "bob" || h.Subject != "hi" || h.Path != "/q/A1" {
		t.Fatalf("headers = %+v", h)
	}
	if st, _, _ := be.Status(entries[0]); st != "Fake" {
		t.Fatalf("status = %q", st)
	}
	body, err := be.Body(entries[0], 1024)
	if err != nil || string(body) != "line1\nline2\n" {
		t.Fatalf("body = %q %v", body, err)
	}
	if _, err := be.Body(backend.Entry{ID: "nope"}, 1024); err == nil {
		t.Fatal("body of unknown message")
	}
	if err := be.Action("B2", backend.ActionDelete); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fake.Actions, []string{"delete B2"}) {
		t.Fatalf("actions = %v", fake.Actions)
	}
	if err := be.SetQueue(1); err != nil {
		t.Fatal(err)
	}
	entries, _ = be.Scan(ctx, 100)
	if len(entries) != 1 || entries[0].ID != "H1" {
		t.Fatalf("queue 1 = %v", entries)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
