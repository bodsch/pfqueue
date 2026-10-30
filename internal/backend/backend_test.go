package backend

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const postcatOut = `*** ENVELOPE RECORDS deferred/A/ABC123 ***
message_size:             345             187               1               0             345
sender:
recipient: bob@example.org
*** MESSAGE CONTENTS deferred/A/ABC123 ***
Received: by mx (Postfix)
From: Alice <alice@example.com>
To: bob@example.org
Subject: =?UTF-8?B?SGFsbG8gV2VsdA==?=
Subject: second subject is ignored

body
`

func parse(hp *headerParser, out string) Headers {
	for l := range strings.SplitSeq(out, "\n") {
		if !hp.feed(l) {
			break
		}
	}
	return hp.result()
}

func TestPostfixHeaders(t *testing.T) {
	h := parse(newPostfixParser(false), postcatOut)
	want := Headers{From: "Alice <alice@example.com>", To: "bob@example.org", Subject: "Hallo Welt", Complete: true}
	if h != want {
		t.Fatalf("headers = %+v", h)
	}

	h = parse(newPostfixParser(true), postcatOut)
	if h.From != nullEnvSender || h.To != "bob@example.org" || !h.Complete {
		t.Fatalf("envelope = %+v", h)
	}

	h = parse(newPostfixParser(false), "From: a\n")
	if h.Complete || h.To != StrNotFound {
		t.Fatalf("incomplete = %+v", h)
	}
}

func TestEximHeaders(t *testing.T) {
	out := "1abcDE-000001-AB-H\nroot 0 0\n<alice@example.com>\n" +
		"045P Received: from x\n" +
		"030F From: alice@example.com\n" +
		"025T To: bob@example.org\n" +
		"015  Subject: hi\n"
	h := parse(newHeaderParser(headerFrom, headerTo, "Null sender", eximHeaderSkip), out)
	want := Headers{From: "alice@example.com", To: "bob@example.org", Subject: "hi", Complete: true}
	if h != want {
		t.Fatalf("headers = %+v", h)
	}
	if v := parseEximOption("spool_directory = /var/spool/exim4"); v != "/var/spool/exim4" {
		t.Fatalf("option = %q", v)
	}
}

func TestDigDir(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"A/1-H", "A/1-D", "B/2-H", ".hidden/3-H", "4-H", ".5-H"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "B"), filepath.Join(dir, "L")); err != nil {
		t.Fatal(err)
	}

	accept := func(name string) (string, bool) { return strings.CutSuffix(name, "-H") }
	var got []string
	for _, e := range digDir(context.Background(), dir, 100, accept) {
		got = append(got, e.ID+"@"+strings.TrimPrefix(e.Path, dir))
	}
	want := []string{"4@/4-H", "1@/A/1-H", "2@/B/2-H", "2@/L/2-H"}
	if !slices.Equal(got, want) {
		t.Fatalf("dig = %v, want %v", got, want)
	}
	if n := len(digDir(context.Background(), dir, 2, accept)); n != 2 {
		t.Fatalf("max not honoured: %d", n)
	}
}

func TestPostfixDeferStatus(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "defer", "A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "defer", "A", "ABC"),
		[]byte("recipient=bob@x\nreason=connect to mx: Connection refused\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := newPostfix()
	p.version = pfVersion22
	p.queueDir = dir
	e := Entry{ID: "ABC", Path: filepath.Join(dir, "deferred", "A", "ABC")}
	if st, _, _ := p.Status(e); st != "connect to mx: Connection refused" {
		t.Fatalf("status = %q", st)
	}
	e.Path = filepath.Join(dir, "deferred", "A", "XYZ")
	if st, _, _ := p.Status(e); st != deferNoReason {
		t.Fatalf("missing defer = %q", st)
	}
	if err := p.SetQueue(QueueHold); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := p.Status(e); st != "Held" {
		t.Fatalf("hold status = %q", st)
	}
}
