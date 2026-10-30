// pfqueue is a realtime queue scanner and manager for postfix and exim.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/syslog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"pfqueue/internal/config"
	"pfqueue/internal/pfq"
	"pfqueue/internal/server"
	"pfqueue/internal/tui"
)

// Version is the program version; may be set with -ldflags "-X main.Version=...".
var Version = "0.7.0"

// Exit codes of the C version.
const (
	exitFrontend = -2
	exitNotRoot  = -3
	exitBackend  = -4
)

func version() {
	fmt.Printf("pfqueue - Version %s - (C) 2004-2009 Stefano Rivoir, 2026 Bodo Schulz\n", Version)
}

func usage() {
	version()
	fmt.Print("Usage: pfqueue [-ehvn] [-q queue_num] [-m max_msg]\n" +
		"       [-b postfix2|exim|socket] [-B backends_path]\n" +
		"       [-f ncurses|socket] [-F frontends_path]\n" +
		"       [-s autorefresh_seconds] [-l limit_seconds] [-d scan_delay]\n" +
		"       [-p executables_path] [-c config_path] [-r remote_host]\n" +
		"       [-L listen_address]\n")
}

func main() { os.Exit(run(os.Args[1:])) }

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func run(args []string) int {
	opts, _, err := getopt(args, optSpec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pfqueue: %v\n", err)
		usage()
		return 1
	}
	// help and version need no privileges
	for _, o := range opts {
		switch o.name {
		case 'h':
			usage()
			return 0
		case 'v':
			version()
			return 0
		}
	}

	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr, "You need to be root to use pfqueue, sorry")
		return exitNotRoot
	}

	conf := pfq.DefaultConfig()
	st := pfq.DefaultStatus()
	if err := config.Load(&conf, &st); err != nil {
		fmt.Fprintf(os.Stderr, "pfqueue: config: %v\n", err)
	}
	if err := applyOptions(opts, &conf, &st); err != nil {
		fmt.Fprintf(os.Stderr, "pfqueue: %v\n", err)
		usage()
		return 1
	}

	switch conf.FrontendName {
	case "ncurses", "tui", "socket":
	default:
		fmt.Fprintf(os.Stderr, "pfqueue: unknown frontend %q\n", conf.FrontendName)
		return exitFrontend
	}

	logger := newLogger(conf.FrontendName == "socket")

	c := pfq.New(conf, st, logger)
	if err := c.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "pfqueue: cannot start backend: %v\n", err)
		c.Close()
		return exitBackend
	}
	defer c.Close()

	if conf.FrontendName == "socket" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		addr := listenAddr(conf.ListenAddress, conf.RemotePort)
		logger.Printf("socket frontend listening on %s", addr)
		if err := server.New(c, logger).ListenAndServe(ctx, addr); err != nil {
			fmt.Fprintf(os.Stderr, "pfqueue: cannot start frontend: %v\n", err)
			return exitFrontend
		}
		logger.Printf("OK, quitting")
		return 0
	}

	if err := tui.Run(c, tui.Info{Version: Version}); err != nil {
		fmt.Fprintf(os.Stderr, "pfqueue: cannot start frontend: %v\n", err)
		return exitFrontend
	}
	return 0
}

// applyOptions applies the command line options to conf and st. Numeric
// arguments must be numbers; out of range values are clamped.
func applyOptions(opts []option, conf *pfq.Config, st *pfq.Status) error {
	for _, o := range opts {
		n := 0
		if strings.ContainsRune(numericOpts, rune(o.name)) {
			var err error
			if n, err = strconv.Atoi(o.arg); err != nil {
				return fmt.Errorf("option -%c: %q is not a number", o.name, o.arg)
			}
		}
		switch o.name {
		case 'p':
			conf.BackendProgs = o.arg
		case 'c':
			conf.BackendConfig = o.arg
		case 'r':
			conf.RemoteHost = o.arg
		case 'L':
			conf.ListenAddress = o.arg
		case 'B':
			conf.BackendsPath = o.arg
		case 'F':
			conf.FrontendsPath = o.arg
		case 'b':
			conf.BackendName = o.arg
		case 'f':
			conf.FrontendName = o.arg
		case 'q':
			conf.InitialQueue = pfq.MapQueueArg(n)
		case 'm':
			conf.MaxMessages = max(n, 5)
		case 's':
			conf.Refresh = clamp(n, 1, 300)
		case 'l':
			conf.ScanLimit = clamp(n, 1, 300)
		case 'd':
			conf.ScanDelay = max(n, 1)
		case 'e':
			st.UseEnvelope = true
		case 'n':
			st.UseColors = false
		}
	}
	return nil
}

// optSpec lists the options in getopt(3) syntax.
const optSpec = "p:c:r:L:B:F:b:f:q:s:m:l:d:ehvn"

// listenAddr returns the address the socket frontend listens on. addr is
// empty (all interfaces), a host or IP address, or host:port; without a
// port, port is used.
func listenAddr(addr string, port int) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(strings.Trim(addr, "[]"), strconv.Itoa(port))
}

// numericOpts are the options taking a number.
const numericOpts = "qmsld"

// newLogger logs to syslog (LOG_USER|LOG_ERR, like the C version) and,
// for the headless socket frontend, also to stderr.
func newLogger(stderr bool) *log.Logger {
	var w []io.Writer
	if sl, err := syslog.New(syslog.LOG_USER|syslog.LOG_ERR, "pfqueue"); err == nil {
		w = append(w, sl)
	}
	if stderr {
		w = append(w, os.Stderr)
	}
	return log.New(io.MultiWriter(w...), "", 0)
}
