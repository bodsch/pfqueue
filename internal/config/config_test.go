package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pfqueue/internal/pfq"
)

func TestParse(t *testing.T) {
	in := `# comment
backend_name = postfix2
mta_config=/etc/postfix
 max_messages = 500
scan_delay=0
use_envelope=yes
use_colors=no
default_queue=4
autorefresh=3
remote_host = mx.example.org
listen_address = 127.0.0.1
garbage
`
	conf, st := pfq.DefaultConfig(), pfq.DefaultStatus()
	Parse(strings.NewReader(in), &conf, &st)

	if conf.BackendName != "postfix2" || conf.BackendConfig != "/etc/postfix" {
		t.Errorf("backend = %q %q", conf.BackendName, conf.BackendConfig)
	}
	if conf.MaxMessages != 500 || conf.ScanDelay != 1 || conf.Refresh != 3 {
		t.Errorf("numbers = %d %d %d", conf.MaxMessages, conf.ScanDelay, conf.Refresh)
	}
	if !st.UseEnvelope || st.UseColors {
		t.Errorf("flags = %v %v", st.UseEnvelope, st.UseColors)
	}
	if conf.ListenAddress != "127.0.0.1" {
		t.Errorf("listen_address = %q", conf.ListenAddress)
	}
	if conf.InitialQueue != 1 || conf.RemoteHost != "mx.example.org" {
		t.Errorf("queue/host = %d %q", conf.InitialQueue, conf.RemoteHost)
	}
}

func TestParseIgnoresInvalidValues(t *testing.T) {
	conf, st := pfq.DefaultConfig(), pfq.DefaultStatus()
	Parse(strings.NewReader("max_messages=lots\nuse_colors=maybe\ndefault_queue=-1\nremote_port=2525\n"), &conf, &st)
	def := pfq.DefaultConfig()
	if conf.MaxMessages != def.MaxMessages || !st.UseColors || conf.InitialQueue != 0 {
		t.Errorf("invalid values applied: %+v %+v", conf, st)
	}
	if conf.RemotePort != 2525 {
		t.Errorf("remote_port = %d", conf.RemotePort)
	}
}

func writeConf(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "pfqueue.conf")
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// explicit chmod: WriteFile is subject to the umask
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLoad(t *testing.T) {
	system := writeConf(t, "max_messages=300\nmta_bin=/usr/sbin\n", 0o644)
	user := writeConf(t, "max_messages=400\n", 0o600)

	conf, st := pfq.DefaultConfig(), pfq.DefaultStatus()
	if err := load(&conf, &st, system, user); err != nil {
		t.Fatal(err)
	}
	if conf.ConfigFrom != 3 || conf.MaxMessages != 400 || conf.BackendProgs != "/usr/sbin" {
		t.Fatalf("conf = %+v", conf)
	}

	conf = pfq.DefaultConfig()
	missing := filepath.Join(t.TempDir(), "none")
	if err := load(&conf, &st, missing, ""); err != nil || conf.ConfigFrom != 0 {
		t.Fatalf("missing files: %v, from %d", err, conf.ConfigFrom)
	}
}

func TestLoadRejectsWritableFile(t *testing.T) {
	// anyone who can write the file could point mta_bin at their own
	// programs, which pfqueue runs as root
	for _, mode := range []os.FileMode{0o666, 0o664, 0o646} {
		evil := writeConf(t, "mta_bin=/tmp/evil\n", mode)
		conf, st := pfq.DefaultConfig(), pfq.DefaultStatus()
		err := load(&conf, &st, "", evil)
		if !errors.Is(err, ErrUnsafe) {
			t.Errorf("mode %v: err = %v", mode, err)
		}
		if conf.BackendProgs != "" || conf.ConfigFrom != 0 {
			t.Errorf("mode %v: unsafe file applied: %+v", mode, conf)
		}
	}
}
