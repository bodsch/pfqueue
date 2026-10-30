// Package config reads /etc/pfqueue.conf and ~/.pfqueue (pfqconfig.c).
//
// Lines have the form "option=value"; all whitespace is removed and lines
// starting with '#' are ignored.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"pfqueue/internal/pfq"
)

// SystemFile is the global configuration file.
const SystemFile = "/etc/pfqueue.conf"

// ErrUnsafe is returned for a configuration file that someone other than
// root or the current user could have written.
var ErrUnsafe = errors.New("writable by other users, ignored")

// Load reads the system and the user configuration file, the latter
// overriding the former, and records which ones were found. Files that
// cannot be read or are unsafe are skipped and reported in the error.
func Load(conf *pfq.Config, st *pfq.Status) error {
	user := ""
	if home, err := os.UserHomeDir(); err == nil {
		user = filepath.Join(home, ".pfqueue")
	}
	return load(conf, st, SystemFile, user)
}

func load(conf *pfq.Config, st *pfq.Status, system, user string) error {
	conf.ConfigFrom = 0
	ok, err1 := readFile(system, conf, st)
	if ok {
		conf.ConfigFrom |= 1
	}
	var err2 error
	if user != "" {
		if ok, err2 = readFile(user, conf, st); ok {
			conf.ConfigFrom |= 2
		}
	}
	return errors.Join(err1, err2)
}

// readFile applies the file name. A missing file is not an error.
func readFile(name string, conf *pfq.Config, st *pfq.Status) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }() // read only, nothing to flush
	// pfqueue runs as root and the file names the MTA tools it executes
	// (mta_bin), so a file another user can write would hand them root.
	if err := checkSafe(f); err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	Parse(f, conf, st)
	return true, nil
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
	case "listen_address":
		conf.ListenAddress = val
	}
}
