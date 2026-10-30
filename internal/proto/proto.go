// Package proto defines the line based spfqueue protocol spoken between the
// socket frontend (server) and the socket backend (client).
//
// Requests are "<CMD> [arg]\n". Replies are "RESULT: <value>\n" or
// "ERROR-: <reason>\n". BODY replies are "RESULT: NNNNNN,<body>\n" where
// NNNNNN is the body length in bytes.
package proto

const (
	CmdNumMsg  = "NUMMSG"
	CmdNumQ    = "NUMQ"
	CmdQName   = "QNAME"
	CmdSetQ    = "SETQ"
	CmdListIDs = "LSTIDS"
	CmdPath    = "MSGPATH"
	CmdFrom    = "MSGFROM"
	CmdTo      = "MSGTO"
	CmdSubj    = "MSGSUBJ"
	CmdMsgID   = "MSGID"
	CmdStatus  = "MSGSTAT"
	CmdQuit    = "QUIT"
	CmdBody    = "BODY"
	CmdLastChg = "LASTC"

	CmdMsgDel  = "MSGDEL"
	CmdMsgReq  = "MSGREQ"
	CmdMsgRel  = "MSGREL"
	CmdMsgHold = "MSGHOLD"

	Reply      = "RESULT"
	ReplyError = "ERROR-"
	// ReplyLen is the length of "RESULT: ".
	ReplyLen = 8

	DefaultPort = 20000
)
