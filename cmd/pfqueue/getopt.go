package main

import (
	"fmt"
	"strings"
)

type option struct {
	name byte
	arg  string
}

// getopt parses args like POSIX getopt(3): options may be clustered
// ("-ehn"), arguments may be attached ("-q1") or separate ("-q 1"), and
// parsing stops at "--" or the first non-option argument.
func getopt(args []string, spec string) ([]option, []string, error) {
	var opts []option
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return opts, args[i+1:], nil
		}
		if len(a) < 2 || a[0] != '-' {
			return opts, args[i:], nil
		}
		for j := 1; j < len(a); j++ {
			c := a[j]
			k := strings.IndexByte(spec, c)
			if c == ':' || k < 0 {
				return nil, nil, fmt.Errorf("invalid option -- '%c'", c)
			}
			if k+1 >= len(spec) || spec[k+1] != ':' {
				opts = append(opts, option{name: c})
				continue
			}
			switch {
			case j+1 < len(a):
				opts = append(opts, option{name: c, arg: a[j+1:]})
			case i+1 < len(args):
				i++
				opts = append(opts, option{name: c, arg: args[i]})
			default:
				return nil, nil, fmt.Errorf("option requires an argument -- '%c'", c)
			}
			break
		}
	}
	return opts, nil, nil
}
