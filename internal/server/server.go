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

	"pfqueue/internal/backend"
	"pfqueue/internal/pfq"
	"pfqueue/internal/proto"
)

const (
	Name    = "socket"
	Version = "1.0"
)

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

// Serve accepts clients on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close() // unblocks Accept; shutting down anyway
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	s.log.Printf("client connected: %s", conn.RemoteAddr())
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.log.Printf("client %s: %v", conn.RemoteAddr(), err)
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !s.process(w, line) {
			_ = w.Flush() // client is leaving, a lost reply does not matter
			s.log.Printf("client disconnected: %s", conn.RemoteAddr())
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

// Replies go to a bufio.Writer, whose errors are sticky and reported by
// the Flush in handle; the individual writes need no check.

func reply(w io.Writer, v string) { _, _ = fmt.Fprintf(w, "%s: %s\n", proto.Reply, v) }
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
