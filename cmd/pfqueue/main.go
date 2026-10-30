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
		"       [-p executables_path] [-c config_path] [-r remote_host]\n")
}

func main() { os.Exit(run(os.Args[1:])) }

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

func run(args []string) int {
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr, "You need to be root to use pfqueue, sorry")
		return exitNotRoot
	}

	conf := pfq.DefaultConfig()
	st := pfq.DefaultStatus()
	config.Load(&conf, &st)

	opts, _, err := getopt(args, "p:c:r:B:F:b:f:q:s:m:l:d:ehvn")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pfqueue: %v\n", err)
		usage()
		return 1
	}
	for _, o := range opts {
		n, _ := strconv.Atoi(o.arg)
		switch o.name {
		case 'p':
			conf.BackendProgs = o.arg
		case 'c':
			conf.BackendConfig = o.arg
		case 'r':
			conf.RemoteHost = o.arg
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
		case 'h':
			usage()
			return 0
		case 'v':
			version()
			return 0
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
		addr := net.JoinHostPort("", strconv.Itoa(conf.RemotePort))
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
