package main

import (
	"os"
	"testing"

	"pfqueue/internal/pfq"
)

func TestApplyOptions(t *testing.T) {
	opts, _, err := getopt([]string{"-q2", "-m", "1", "-s", "999", "-l0", "-d", "-5", "-en", "-b", "exim", "-r", "mx:2000", "-L", "127.0.0.1"}, optSpec)
	if err != nil {
		t.Fatal(err)
	}
	conf, st := pfq.DefaultConfig(), pfq.DefaultStatus()
	if err := applyOptions(opts, &conf, &st); err != nil {
		t.Fatal(err)
	}
	if conf.InitialQueue != 3 || conf.MaxMessages != 5 || conf.Refresh != 300 ||
		conf.ScanLimit != 1 || conf.ScanDelay != 1 {
		t.Errorf("numbers = %+v", conf)
	}
	if !st.UseEnvelope || st.UseColors || conf.BackendName != "exim" || conf.RemoteHost != "mx:2000" ||
		conf.ListenAddress != "127.0.0.1" {
		t.Errorf("flags = %+v %+v", conf, st)
	}
}

func TestApplyOptionsRejectsNonNumbers(t *testing.T) {
	for _, o := range []option{{'q', "x"}, {'m', ""}, {'s', "1s"}, {'l', "ten"}, {'d', "1.5"}} {
		conf, st := pfq.DefaultConfig(), pfq.DefaultStatus()
		if err := applyOptions([]option{o}, &conf, &st); err == nil {
			t.Errorf("-%c %q accepted", o.name, o.arg)
		}
	}
}

func TestHelpAndVersionNeedNoRoot(t *testing.T) {
	// run checks for root only after -h and -v have been handled
	for _, args := range [][]string{{"-h"}, {"-v"}} {
		if code := run(args); code != 0 {
			t.Errorf("run(%v) = %d", args, code)
		}
	}
	if code := run([]string{"-x"}); code != 1 {
		t.Errorf("run(-x) = %d", code)
	}
}

func TestListenAddr(t *testing.T) {
	for in, want := range map[string]string{
		"":              ":20000",
		"127.0.0.1":     "127.0.0.1:20000",
		"localhost":     "localhost:20000",
		"::1":           "[::1]:20000",
		"[::1]":         "[::1]:20000",
		"10.0.0.5:2525": "10.0.0.5:2525",
		"[::1]:2525":    "[::1]:2525",
		":2525":         ":2525",
	} {
		if got := listenAddr(in, 20000); got != want {
			t.Errorf("listenAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNotRootExitCode checks the README's exit code 253 (-3) for a non-root
// user; scripts and monitoring tell "wrong user" from "MTA broken" by it.
func TestNotRootExitCode(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	if code := run(nil); code != exitNotRoot || uint8(code) != 253 {
		t.Fatalf("run as non-root = %d", code)
	}
}
