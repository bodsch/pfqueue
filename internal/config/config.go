// Package config reads /etc/pfqueue.conf and ~/.pfqueue (pfqconfig.c).
//
// Lines have the form "option=value"; all whitespace is removed and lines
// starting with '#' are ignored.
package config

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"pfqueue/internal/pfq"
)

// SystemFile is the global configuration file.
const SystemFile = "/etc/pfqueue.conf"

// Load reads the system and the user configuration file, the latter
// overriding the former, and records which ones were found.
func Load(conf *pfq.Config, st *pfq.Status) {
	conf.ConfigFrom = 0
	if readFile(SystemFile, conf, st) {
		conf.ConfigFrom |= 1
	}
	if home, err := os.UserHomeDir(); err == nil {
		if readFile(filepath.Join(home, ".pfqueue"), conf, st) {
			conf.ConfigFrom |= 2
		}
	}
}

func readFile(name string, conf *pfq.Config, st *pfq.Status) bool {
	f, err := os.Open(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }() // read only, nothing to flush
	Parse(f, conf, st)
	return true
}

// Parse applies the options read from r.
func Parse(r io.Reader, conf *pfq.Config, st *pfq.Status) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		row := strings.Map(func(c rune) rune {
			if c <= ' ' {
				return -1
			}
			return c
		}, sc.Text())
		if row == "" || row[0] == '#' {
			continue
		}
		opt, val, ok := strings.Cut(row, "=")
		if !ok {
			continue
		}
		apply(opt, val, conf, st)
	}
}

func apply(opt, val string, conf *pfq.Config, st *pfq.Status) {
	num := func(dst *int) {
		if n, err := strconv.Atoi(val); err == nil && n != 0 {
			*dst = n
		}
	}
	flag := func(dst *bool) {
		switch val {
		case "yes":
			*dst = true
		case "no":
			*dst = false
		}
	}
	switch opt {
	case "backends_path":
		conf.BackendsPath = val
	case "backend_name":
		conf.BackendName = val
	case "frontends_path":
		conf.FrontendsPath = val
	case "frontend_name":
		conf.FrontendName = val
	case "mta_config":
		conf.BackendConfig = val
	case "mta_bin":
		conf.BackendProgs = val
	case "max_messages":
		num(&conf.MaxMessages)
	case "scan_delay":
		num(&conf.ScanDelay)
	case "scan_limit":
		num(&conf.ScanLimit)
	case "autorefresh":
		num(&conf.Refresh)
	case "use_envelope":
		flag(&st.UseEnvelope)
	case "use_colors":
		flag(&st.UseColors)
	case "default_queue":
		if n, err := strconv.Atoi(val); err == nil && n >= 0 {
			conf.InitialQueue = pfq.MapQueueArg(n)
		}
	case "remote_host":
		conf.RemoteHost = val
	case "remote_port":
		num(&conf.RemotePort)
	}
}
