package backend

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"pfqueue/internal/proto"
)

// socket talks to a remote pfqueue running the socket frontend.
type socket struct {
	mu     sync.Mutex
	conn   net.Conn
	r      *bufio.Reader
	queues []string
}

func newSocket() *socket { return &socket{} }

func (s *socket) ID() string      { return "socket" }
func (s *socket) Version() string { return "1.0" }

func (s *socket) Caps() Caps { return CapHold | CapDelete | CapRequeue }

func (s *socket) Setup(c Config) error {
	if c.Host == "" {
		return fmt.Errorf("%w: no remote host given (-r)", ErrUnusable)
	}
	addr := c.Host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		port := c.Port
		if port == 0 {
			port = proto.DefaultPort
		}
		addr = net.JoinHostPort(c.Host, strconv.Itoa(port))
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnusable, err)
	}
	s.conn = conn
	s.r = bufio.NewReader(conn)

	n, err := s.requestInt(proto.CmdNumQ)
	if err != nil {
		_ = conn.Close() // the protocol error is what matters
		return fmt.Errorf("%w: %v", ErrUnusable, err)
	}
	for i := 0; i < n; i++ {
		name, err := s.request(fmt.Sprintf("%s %d", proto.CmdQName, i))
		if err != nil {
			name = "noname"
		}
		s.queues = append(s.queues, name)
	}
	return nil
}

func (s *socket) Close() error {
	if s.conn == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// best effort: the server also cleans up when the connection drops
	_, _ = fmt.Fprintf(s.conn, "%s\n", proto.CmdQuit)
	return s.conn.Close()
}

// request sends a command and returns the reply value.
func (s *socket) request(cmd string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.conn, "%s\n", cmd); err != nil {
		return "", err
	}
	line, err := s.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return parseReply(strings.TrimRight(line, "\r\n"))
}

func (s *socket) requestInt(cmd string) (int, error) {
	v, err := s.request(cmd)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(v))
}

func parseReply(line string) (string, error) {
	if strings.HasPrefix(line, "ERR") {
		return "", errors.New(line)
	}
	if len(line) < proto.ReplyLen {
		return "", nil
	}
	return line[proto.ReplyLen:], nil
}

func (s *socket) QueueCount() int { return len(s.queues) }

func (s *socket) QueueName(q int) string {
	if q < 0 || q >= len(s.queues) {
		return "noname"
	}
	return s.queues[q]
}

func (s *socket) SetQueue(q int) error {
	_, err := s.request(fmt.Sprintf("%s %d", proto.CmdSetQ, q))
	return err
}

func (s *socket) Scan(ctx context.Context, max int) ([]Entry, error) {
	n, err := s.requestInt(proto.CmdNumMsg)
	if err != nil {
		return nil, err
	}
	n = min(n, max)
	out := make([]Entry, 0, n)
	for i := 0; i < n && ctx.Err() == nil; i++ {
		id, err := s.request(fmt.Sprintf("%s %d", proto.CmdMsgID, i))
		if err != nil {
			// the remote queue shrank meanwhile
			break
		}
		out = append(out, Entry{ID: id})
	}
	return out, nil
}

func (s *socket) field(cmd, id string) string {
	v, err := s.request(cmd + " " + id)
	if err != nil {
		return StrError
	}
	return v
}

func (s *socket) Headers(e Entry, _ bool) (Headers, error) {
	return Headers{
		To:       s.field(proto.CmdTo, e.ID),
		From:     s.field(proto.CmdFrom, e.ID),
		Subject:  s.field(proto.CmdSubj, e.ID),
		Path:     s.field(proto.CmdPath, e.ID),
		Complete: true,
	}, nil
}

func (s *socket) Status(e Entry) (string, bool, error) {
	return s.field(proto.CmdStatus, e.ID), false, nil
}

func (s *socket) Body(e Entry, max int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.conn, "%s %s\n", proto.CmdBody, e.ID); err != nil {
		return nil, err
	}
	head := make([]byte, proto.ReplyLen)
	if _, err := io.ReadFull(s.r, head); err != nil {
		return nil, err
	}
	if strings.HasPrefix(string(head), "ERR") {
		rest, _ := s.r.ReadString('\n')
		return nil, errors.New(string(head) + strings.TrimSpace(rest))
	}
	lenStr, err := s.r.ReadString(',')
	if err != nil {
		return nil, err
	}
	n, err := strconv.Atoi(strings.TrimSuffix(lenStr, ","))
	if err != nil || n < 0 {
		return nil, fmt.Errorf("bad body length %q", lenStr)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(s.r, body); err != nil {
		return nil, err
	}
	// trailing newline terminating the reply
	if _, err := s.r.ReadString('\n'); err != nil {
		return nil, err
	}
	if len(body) > max {
		body = body[:max]
	}
	return body, nil
}

func (s *socket) Action(id string, a Action) error {
	var cmd string
	switch a {
	case ActionDelete:
		cmd = proto.CmdMsgDel
	case ActionHold:
		cmd = proto.CmdMsgHold
	case ActionRelease:
		cmd = proto.CmdMsgRel
	case ActionRequeue:
		cmd = proto.CmdMsgReq
	default:
		return ErrUnusable
	}
	_, err := s.request(cmd + " " + id)
	return err
}
