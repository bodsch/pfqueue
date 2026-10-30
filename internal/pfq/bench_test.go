package pfq

import (
	"fmt"
	"testing"

	"pfqueue/internal/backend/fakebe"
)

func bigQueue(n int) []fakebe.Msg {
	msgs := make([]fakebe.Msg, n)
	for i := range msgs {
		msgs[i] = fakebe.Msg{
			ID:      fmt.Sprintf("ID%06d", i),
			From:    fmt.Sprintf("sender%d@example.org", i%97),
			To:      fmt.Sprintf("rcpt%d@example.com", i%89),
			Subject: fmt.Sprintf("subject %d", i),
		}
	}
	return msgs
}

func startBench(b *testing.B, n int) *Context {
	b.Helper()
	conf := DefaultConfig()
	conf.ScanDelay = 3600
	conf.MaxMessages = n
	c := New(conf, DefaultStatus(), nil)
	if err := c.StartWith(fakebe.New(bigQueue(n))); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(c.Close)
	return c
}

// BenchmarkSearchMiss walks a whole queue without a match, which looks up
// every message by id once per step.
func BenchmarkSearchMiss(b *testing.B) {
	c := startBench(b, 5000)
	if err := c.SetSearch("no-such-thing"); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		if c.SearchFirst() != -1 {
			b.Fatal("unexpected match")
		}
	}
}

// BenchmarkMessage looks up single messages by id.
func BenchmarkMessage(b *testing.B) {
	c := startBench(b, 5000)
	for b.Loop() {
		if _, ok := c.Message("ID004999"); !ok {
			b.Fatal("not found")
		}
	}
}

// BenchmarkRescan merges an unchanged queue, the common case of the
// periodic scan.
func BenchmarkRescan(b *testing.B) {
	c := startBench(b, 5000)
	for b.Loop() {
		c.scanOnce(true)
	}
}
