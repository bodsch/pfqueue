package pfq

import (
	"slices"
	"testing"

	"pfqueue/internal/backend"
	"pfqueue/internal/backend/fakebe"
)

func ids(c *Context) []string {
	var out []string
	for _, m := range c.Snapshot() {
		out = append(out, m.ID)
	}
	return out
}

func start(t *testing.T, st Status, queues ...[]fakebe.Msg) (*Context, *fakebe.Backend) {
	t.Helper()
	be := fakebe.New(queues...)
	conf := DefaultConfig()
	conf.ScanDelay = 3600 // scans are triggered explicitly
	c := New(conf, st, nil)
	if err := c.StartWith(be); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, be
}

var sample = []fakebe.Msg{
	{ID: "A1", From: "carol@x", To: "bob@y", Subject: "zeta"},
	{ID: "B2", From: "alice@x", To: "dave@y", Subject: "alpha"},
	{ID: "C3", From: "bob@x", To: "erin@y", Subject: "spam offer"},
}

func TestScanAndSetQueue(t *testing.T) {
	c, _ := start(t, DefaultStatus(), sample, []fakebe.Msg{{ID: "H1"}})
	if got := ids(c); !slices.Equal(got, []string{"A1", "B2", "C3"}) {
		t.Fatalf("queue 0 = %v", got)
	}
	if err := c.SetQueue(1); err != nil {
		t.Fatal(err)
	}
	if got := ids(c); !slices.Equal(got, []string{"H1"}) {
		t.Fatalf("queue 1 = %v", got)
	}
	if err := c.SetQueue(2); err == nil {
		t.Fatal("SetQueue(2) should fail")
	}
}

func TestTagsSurviveRescan(t *testing.T) {
	c, be := start(t, DefaultStatus(), slices.Clone(sample))
	c.Tag("B2")
	c.RetrHeaders("B2")
	be.Set(0, append(slices.Clone(sample), fakebe.Msg{ID: "D4"}))
	// forces a scan, but also resets the queue
	if err := c.SetQueue(0); err != nil {
		t.Fatal(err)
	}
	if c.NumTagged() != 0 {
		t.Fatal("SetQueue should reset tags")
	}

	c.Tag("B2")
	c.scanOnce(true)
	if got := ids(c); !slices.Equal(got, []string{"A1", "B2", "C3", "D4"}) {
		t.Fatalf("after rescan = %v", got)
	}
	if m, _ := c.Message("B2"); !m.Tagged {
		t.Fatal("tag lost on rescan")
	}
}

func TestActionOnTagged(t *testing.T) {
	c, be := start(t, DefaultStatus(), slices.Clone(sample))

	if err := c.Action("A1", backend.ActionHold); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.Actions, []string{"hold A1"}) {
		t.Fatalf("single action = %v", be.Actions)
	}

	be.Actions = nil
	c.TagRange(2, 1)
	c.UpdateStatus(func(s *Status) { s.WorkTagged = true })
	if !c.ActsOnTagged() {
		t.Fatal("ActsOnTagged = false")
	}
	if err := c.Action("A1", backend.ActionDelete); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.Actions, []string{"delete B2", "delete C3"}) {
		t.Fatalf("tagged action = %v", be.Actions)
	}
	if c.NumTagged() != 0 || c.Status().WorkTagged {
		t.Fatal("tags / work-on-tagged not reset")
	}

	// auto-work-on-tagged only applies when something is tagged
	be.Actions = nil
	c.UpdateStatus(func(s *Status) { s.AutoWorkTagged = true })
	if err := c.Action("A1", backend.ActionRequeue); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(be.Actions, []string{"requeue A1"}) {
		t.Fatalf("auto action = %v", be.Actions)
	}
}

func TestSearch(t *testing.T) {
	c, _ := start(t, DefaultStatus(), sample)

	if err := c.SetSearch("s:^(zeta|alpha)$"); err != nil {
		t.Fatal(err)
	}
	if i := c.SearchFirst(); i != 0 {
		t.Fatalf("first = %d", i)
	}
	if i := c.SearchNext(); i != 1 {
		t.Fatalf("next = %d", i)
	}
	if i := c.SearchNext(); i != -1 {
		t.Fatalf("next past end = %d", i)
	}

	if err := c.SetSearch("f:bob"); err != nil {
		t.Fatal(err)
	}
	if i := c.SearchFirst(); i != 2 {
		t.Fatalf("from search = %d", i)
	}
	if i := c.SearchPrev(); i != -1 {
		t.Fatalf("prev = %d", i)
	}

	// matches every From
	if err := c.SetSearch("x$"); err != nil {
		t.Fatal(err)
	}
	if n := c.SearchAndTag(); n != 3 || c.NumTagged() != 3 {
		t.Fatalf("search and tag = %d, tagged %d", n, c.NumTagged())
	}

	if err := c.SetSearch("("); err == nil {
		t.Fatal("invalid regexp accepted")
	}
}

func TestSort(t *testing.T) {
	c, _ := start(t, DefaultStatus(), sample)
	c.SetSort(SortFrom, false)
	c.scanOnce(false)
	if got := ids(c); !slices.Equal(got, []string{"B2", "C3", "A1"}) {
		t.Fatalf("sorted by from = %v", got)
	}
	c.SetSort(SortSubject, true)
	c.scanOnce(false)
	if got := ids(c); !slices.Equal(got, []string{"A1", "C3", "B2"}) {
		t.Fatalf("sorted by subject desc = %v", got)
	}
	c.SetSort(SortUnsorted, false)
	c.scanOnce(true)
	if got := ids(c); !slices.Equal(got, []string{"A1", "B2", "C3"}) {
		t.Fatalf("unsorted = %v", got)
	}
}

func TestEnvelopeToggle(t *testing.T) {
	c, _ := start(t, DefaultStatus(), sample)
	c.RetrHeaders("A1")
	c.ToggleEnvelope()
	c.RetrHeaders("A1")
	if m, _ := c.Message("A1"); m.From != "env-carol@x" {
		t.Fatalf("envelope from = %q", m.From)
	}
}

func TestDoScanOff(t *testing.T) {
	st := DefaultStatus()
	c, be := start(t, st, slices.Clone(sample))
	c.UpdateStatus(func(s *Status) { s.DoScan = false })
	be.Set(0, nil)
	c.scanOnce(false)
	if c.Len() != 3 {
		t.Fatal("queue scanned although scanning is suspended")
	}
}

func TestMapQueueArg(t *testing.T) {
	want := map[int]int{1: 0, 2: 3, 3: 2, 4: 1, 9: 0}
	for in, out := range want {
		if got := MapQueueArg(in); got != out {
			t.Errorf("MapQueueArg(%d) = %d, want %d", in, got, out)
		}
	}
}
