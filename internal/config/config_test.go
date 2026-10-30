package config

import (
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
	if conf.InitialQueue != 1 || conf.RemoteHost != "mx.example.org" {
		t.Errorf("queue/host = %d %q", conf.InitialQueue, conf.RemoteHost)
	}
}
