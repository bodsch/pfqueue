package pfq

import (
	"context"
	"errors"
	"testing"
	"time"

	"pfqueue/internal/backend"
	"pfqueue/internal/backend/fakebe"
)

func TestParseSearch(t *testing.T) {
	for in, want := range map[string]struct {
		expr string
		mode int
	}{
		"f:bob":   {"bob", SearchFrom},
		"t:bob":   {"bob", SearchTo},
		"e:bob":   {"bob", SearchFrom | SearchTo},
		"s:bob":   {"bob", SearchSubject},
		"bob":     {"bob", SearchAll},
		"x:bob":   {"x:bob", SearchAll},
		"f:":      {"", SearchFrom},
		"s:f:bob": {"f:bob", SearchSubject},
	} {
		expr, mode := ParseSearch(in)
		if expr != want.expr || mode != want.mode {
			t.Errorf("ParseSearch(%q) = %q %d", in, expr, mode)
		}
	}
}

func TestSortFieldString(t *testing.T) {
	if SortTo.String() != "sorted by to" || SortField(9).String() != "" || SortField(-1).String() != "" {
		t.Fatal("sort names")
	}
}

func TestRetrBodyUnknown(t *testing.T) {
	c, _ := start(t, DefaultStatus(), sample)
	if _, err := c.RetrBody("nope"); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("err = %v", err)
	}
}

// failing returns an error for every action.
type failing struct{ *fakebe.Backend }

func (failing) Action(string, backend.Action) error { return errors.New("postsuper failed") }

func TestActionErrorsAreJoined(t *testing.T) {
	c := New(DefaultConfig(), DefaultStatus(), nil)
	if err := c.StartWith(failing{fakebe.New(sample)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	c.TagAll()
	c.UpdateStatus(func(s *Status) { s.WorkTagged = true })
	err := c.Action("", backend.ActionDelete)
	if err == nil {
		t.Fatal("no error")
	}
	if n := len(err.(interface{ Unwrap() []error }).Unwrap()); n != 3 {
		t.Fatalf("joined errors = %d: %v", n, err)
	}
}

func TestActionOnUnknownIDDoesNothing(t *testing.T) {
	c, be := start(t, DefaultStatus(), sample)
	if err := c.Action("nope", backend.ActionDelete); err != nil || len(be.Actions) != 0 {
		t.Fatalf("err %v actions %v", err, be.Actions)
	}
}

// dupScan reports every message twice, as a symlinked queue directory does.
type dupScan struct{ *fakebe.Backend }

func (d dupScan) Scan(ctx context.Context, max int) ([]backend.Entry, error) {
	e, err := d.Backend.Scan(ctx, max)
	out := append(e, e...)
	for i := len(e); i < len(out); i++ {
		out[i].Path = "/other" + out[i].Path
	}
	return out, err
}

func TestDuplicateIDsFindFirst(t *testing.T) {
	c := New(DefaultConfig(), DefaultStatus(), nil)
	if err := c.StartWith(dupScan{fakebe.New(sample)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if c.Len() != 6 {
		t.Fatalf("len = %d", c.Len())
	}
	c.Tag("A1")
	if m, _ := c.MessageAt(0); !m.Tagged {
		t.Fatal("first A1 not tagged")
	}
	if m, _ := c.MessageAt(3); m.Tagged {
		t.Fatal("second A1 tagged")
	}
}

func TestCloseWithoutStart(t *testing.T) {
	New(DefaultConfig(), DefaultStatus(), nil).Close() // must not block or panic
}

// noEnvelope is a backend that, like exim and socket, cannot read the
// envelope.
type noEnvelope struct{ *fakebe.Backend }

func (noEnvelope) Caps() backend.Caps { return backend.CapDelete }

// TestEnvelopeNeedsBackendSupport checks "-e: read From/To from the
// envelope (postfix only)". With exim, -e or use_envelope=yes must not
// leave the screen titled "from envelope" over header addresses: an
// operator deleting by sender would be acting on the wrong field.
func TestEnvelopeNeedsBackendSupport(t *testing.T) {
	st := DefaultStatus()
	st.UseEnvelope = true
	c := New(DefaultConfig(), st, nil)
	if err := c.StartWith(noEnvelope{fakebe.New(sample)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if c.Status().UseEnvelope {
		t.Fatal("envelope mode kept for a backend that cannot read the envelope")
	}
	c.ToggleEnvelope()
	if c.Status().UseEnvelope {
		t.Fatal("envelope mode switched on")
	}
}

// slowScan blocks in Scan until its context ends, like a queue directory
// on a hanging NFS mount.
type slowScan struct{ *fakebe.Backend }

func (s slowScan) Scan(ctx context.Context, max int) ([]backend.Entry, error) {
	<-ctx.Done()
	return s.Backend.Scan(context.Background(), 1)
}

// TestScanLimitEndsScan checks "-l: time limit for a single queue scan;
// results may be incomplete". The scan holds the scan lock, so without
// the limit a stuck directory read blocks every action and queue switch.
func TestScanLimitEndsScan(t *testing.T) {
	conf := DefaultConfig()
	conf.ScanLimit = 1
	conf.ScanDelay = 3600
	c := New(conf, DefaultStatus(), nil)
	done := make(chan error, 1)
	go func() { done <- c.StartWith(slowScan{fakebe.New(sample)}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scan with a 1 s limit still running after 5 s")
	}
	t.Cleanup(c.Close)
	if c.Len() != 1 {
		t.Fatalf("partial result not kept: %d messages", c.Len())
	}
}
