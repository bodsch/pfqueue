// Package server implements the socket frontend (spfqueue): it exposes
// the queue of the local MTA over TCP to remote pfqueue instances using
// the socket backend.
package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"pfqueue/internal/backend"
	"pfqueue/internal/pfq"
	"pfqueue/internal/proto"
)

const (
	Name    = "socket"
	Version = "1.0"

	// MaxClients is the number of clients served at the same time; more
	// are disconnected right away.
	MaxClients = 32
	// maxLine bounds a request line. Requests are a command and a message
	// id; a longer line is a broken or hostile client.
	maxLine = 1024
)

// writeTimeout bounds sending a reply to a client that does not read.
// Without it 32 such clients would hold every slot for good. A variable
// for tests.
var writeTimeout = 30 * time.Second

var errLineTooLong = errors.New("request line too long")

// Server serves the spfqueue protocol.
type Server struct {
	ctx *pfq.Context
	log *log.Logger
}

// New returns a server for the library context.
func New(c *pfq.Context, logger *log.Logger) *Server {
	return &Server{ctx: c, log: logger}
}

// ListenAndServe accepts clients on addr until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, ln)
}

// Serve accepts clients on ln until ctx is cancelled. On return all
// client connections are closed and their handlers have finished, so the
// library context may be closed afterwards.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	var (
		mu     sync.Mutex
		conns  = make(map[net.Conn]struct{})
		closed bool
		wg     sync.WaitGroup
	)
	shutdown := func() {
		_ = ln.Close() // unblocks Accept; shutting down anyway
		mu.Lock()
		defer mu.Unlock()
		closed = true
		for c := range conns {
			_ = c.Close() // unblocks the handler's read
		}
	}
	stop := context.AfterFunc(ctx, shutdown)
	defer func() {
		stop()
		shutdown()
		wg.Wait()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		if closed || len(conns) >= MaxClients {
			mu.Unlock()
			s.log.Printf("client %s rejected: too many clients", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}
		conns[conn] = struct{}{}
		mu.Unlock()
		wg.Go(func() {
			defer func() {
				mu.Lock()
				delete(conns, conn)
				mu.Unlock()
			}()
			s.handle(conn)
		})
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	s.log.Printf("client connected: %s", conn.RemoteAddr())
	r := bufio.NewReaderSize(conn, maxLine)
	w := bufio.NewWriter(conn)
	for {
		line, err := readLine(r)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log.Printf("client %s: %v", conn.RemoteAddr(), err)
			}
			return
		}
		quit := !s.process(w, line)
		if err := conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
			return
		}
		if err := w.Flush(); err != nil || quit {
			s.log.Printf("client disconnected: %s", conn.RemoteAddr())
			return
		}
	}
}

// readLine reads one request line without its line ending. A line that
// does not fit the reader's buffer is rejected instead of growing
// memory without bound.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errLineTooLong
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(line), "\r\n"), nil
}

// Replies go to a bufio.Writer, whose errors are sticky and reported by
// the Flush in handle; the individual writes need no check.

// oneLine keeps a value on a single line. Header values are decoded from
// RFC 2047 and may contain line breaks, which would let the sender of a
// mail inject replies into the line based protocol.
var oneLine = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

func reply(w io.Writer, v string) { _, _ = fmt.Fprintf(w, "%s: %s\n", proto.Reply, oneLine.Replace(v)) }
func replyErr(w io.Writer, v string) {
	_, _ = fmt.Fprintf(w, "%s: %s\n", proto.ReplyError, strings.ReplaceAll(v, "\n", "; "))
}

// process handles one request; it returns false when the client quits.
func (s *Server) process(w io.Writer, line string) bool {
	cmd, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	num := func() int { n, _ := strconv.Atoi(arg); return n }

	// msgField replies with a field of message arg after loading it.
	msgField := func(load func(string), get func(pfq.Message) string) {
		if _, ok := s.ctx.Message(arg); !ok {
			replyErr(w, "NOMSG")
			return
		}
		load(arg)
		m, _ := s.ctx.Message(arg)
		reply(w, get(m))
	}
	headers := func(id string) { s.ctx.RetrHeaders(id) }

	switch cmd {
	case proto.CmdNumMsg:
		reply(w, fmt.Sprintf("%05d", s.ctx.Len()))
	case proto.CmdNumQ:
		reply(w, fmt.Sprintf("%05d", s.ctx.NumQueues()))
	case proto.CmdQName:
		reply(w, s.ctx.QueueName(num()))
	case proto.CmdSetQ:
		if err := s.ctx.SetQueue(num()); err != nil {
			replyErr(w, err.Error())
		} else {
			reply(w, "OK")
		}
	case proto.CmdMsgID:
		if m, ok := s.ctx.MessageAt(num()); ok && m.ID != "" {
			reply(w, m.ID)
		} else {
			replyErr(w, "NOMSG")
		}
	case proto.CmdListIDs:
		snap := s.ctx.Snapshot()
		ids := make([]string, len(snap))
		for i, m := range snap {
			ids[i] = m.ID
		}
		reply(w, strings.Join(ids, ","))
	case proto.CmdStatus:
		msgField(s.ctx.RetrStatus, func(m pfq.Message) string { return m.Stat })
	case proto.CmdPath:
		msgField(headers, func(m pfq.Message) string { return m.Path })
	case proto.CmdFrom:
		msgField(headers, func(m pfq.Message) string { return m.From })
	case proto.CmdTo:
		msgField(headers, func(m pfq.Message) string { return m.To })
	case proto.CmdSubj:
		msgField(headers, func(m pfq.Message) string { return m.Subject })
	case proto.CmdBody:
		body, err := s.ctx.RetrBody(arg)
		if err != nil {
			replyErr(w, "NOMSG")
			break
		}
		_, _ = fmt.Fprintf(w, "%s: %06d,", proto.Reply, len(body))
		_, _ = w.Write(body)
		_, _ = io.WriteString(w, "\n")
	case proto.CmdLastChg:
		reply(w, strconv.FormatInt(s.ctx.LastChanged().Unix(), 10))
	case proto.CmdMsgDel:
		s.action(w, arg, backend.ActionDelete)
	case proto.CmdMsgRel:
		s.action(w, arg, backend.ActionRelease)
	case proto.CmdMsgReq:
		s.action(w, arg, backend.ActionRequeue)
	case proto.CmdMsgHold:
		s.action(w, arg, backend.ActionHold)
	case proto.CmdQuit:
		return false
	default:
		_, _ = io.WriteString(w, "ERR: NOCMD\n")
	}
	return true
}

func (s *Server) action(w io.Writer, id string, act backend.Action) {
	if err := s.ctx.Action(id, act); err != nil {
		replyErr(w, err.Error())
		return
	}
	reply(w, "OK")
}
