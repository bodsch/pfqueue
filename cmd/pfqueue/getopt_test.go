package main

import (
	"reflect"
	"testing"
)

func TestGetopt(t *testing.T) {
	const spec = "p:c:q:ehvn"
	opts, rest, err := getopt([]string{"-en", "-q1", "-c", "/etc/postfix", "--", "-x"}, spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []option{{'e', ""}, {'n', ""}, {'q', "1"}, {'c', "/etc/postfix"}}
	if !reflect.DeepEqual(opts, want) || !reflect.DeepEqual(rest, []string{"-x"}) {
		t.Fatalf("opts = %v rest = %v", opts, rest)
	}
	if _, _, err := getopt([]string{"-x"}, spec); err == nil {
		t.Fatal("unknown option accepted")
	}
	if _, _, err := getopt([]string{"-q"}, spec); err == nil {
		t.Fatal("missing argument accepted")
	}
}
