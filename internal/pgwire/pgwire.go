// Package pgwire is a minimal codec for the PostgreSQL v3 wire protocol —
// just the subset needed to broker a connection handshake.
//
// It deliberately avoids buffered readers: every frame is read with exactly
// sized io.ReadFull calls, so after the handshake the connection can be
// handed to a raw byte splice with no risk of losing bytes trapped in a
// read-ahead buffer.
package pgwire

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// Startup-phase codes: the first int32 of a startup packet's payload.
const (
	ProtocolVersion   = 196608 // protocol 3.0
	SSLRequestCode    = 80877103
	CancelRequestCode = 80877102
	GSSEncRequestCode = 80877104
)

// Message type bytes used during the handshake.
const (
	MsgAuth            = byte('R')
	MsgPassword        = byte('p') // also SASLInitialResponse / SASLResponse
	MsgError           = byte('E')
	MsgReadyForQuery   = byte('Z')
	MsgParameterStatus = byte('S')
	MsgBackendKeyData  = byte('K')
	MsgNotice          = byte('N')
)

// Authentication request codes: the first int32 of an 'R' payload.
const (
	AuthOK                = 0
	AuthCleartextPassword = 3
	AuthMD5Password       = 5
	AuthSASL              = 10
	AuthSASLContinue      = 11
	AuthSASLFinal         = 12
)

const (
	// maxStartupLen matches the server-side cap on startup packets.
	maxStartupLen = 10000
	// maxMsgLen caps handshake-phase messages; nothing legitimate comes close.
	maxMsgLen = 1 << 20
)

// Msg is one framed protocol message (type byte + payload, length excluded).
type Msg struct {
	Type    byte
	Payload []byte
}

// ReadStartup reads one startup-phase frame (no type byte) and returns its
// payload.
func ReadStartup(r io.Reader) ([]byte, error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n < 8 || n > maxStartupLen {
		return nil, fmt.Errorf("invalid startup packet length %d", n)
	}
	payload := make([]byte, n-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// ReadMsg reads one regular framed message.
func ReadMsg(r io.Reader) (Msg, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Msg{}, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n < 4 || n > maxMsgLen {
		return Msg{}, fmt.Errorf("invalid message length %d for type %q", n, hdr[0])
	}
	payload := make([]byte, n-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Msg{}, err
	}
	return Msg{Type: hdr[0], Payload: payload}, nil
}

// WriteMsg writes one regular framed message.
func WriteMsg(w io.Writer, m Msg) error {
	buf := make([]byte, 5+len(m.Payload))
	buf[0] = m.Type
	binary.BigEndian.PutUint32(buf[1:], uint32(4+len(m.Payload)))
	copy(buf[5:], m.Payload)
	_, err := w.Write(buf)
	return err
}

// Startup is a parsed startup-phase packet.
type Startup struct {
	Code   uint32
	Params map[string]string // set when Code == ProtocolVersion

	// CancelRequest fields.
	CancelPID    uint32
	CancelSecret uint32
}

// ParseStartup decodes a startup-phase payload as returned by ReadStartup.
func ParseStartup(payload []byte) (*Startup, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("startup packet too short")
	}
	s := &Startup{Code: binary.BigEndian.Uint32(payload[:4])}
	switch s.Code {
	case SSLRequestCode, GSSEncRequestCode:
		return s, nil
	case CancelRequestCode:
		if len(payload) != 12 {
			return nil, fmt.Errorf("invalid cancel request length %d", len(payload))
		}
		s.CancelPID = binary.BigEndian.Uint32(payload[4:8])
		s.CancelSecret = binary.BigEndian.Uint32(payload[8:12])
		return s, nil
	case ProtocolVersion:
		params, err := parseParams(payload[4:])
		if err != nil {
			return nil, err
		}
		s.Params = params
		return s, nil
	default:
		return nil, fmt.Errorf("unsupported protocol version %d.%d", s.Code>>16, s.Code&0xffff)
	}
}

func parseParams(b []byte) (map[string]string, error) {
	params := map[string]string{}
	for len(b) > 0 && b[0] != 0 {
		key, rest, err := cutCString(b)
		if err != nil {
			return nil, err
		}
		val, rest, err := cutCString(rest)
		if err != nil {
			return nil, err
		}
		params[key] = val
		b = rest
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("startup packet missing terminator")
	}
	return params, nil
}

func cutCString(b []byte) (string, []byte, error) {
	before, after, found := bytes.Cut(b, []byte{0})
	if !found {
		return "", nil, fmt.Errorf("unterminated string in packet")
	}
	return string(before), after, nil
}

// EncodeStartup builds a startup packet for the given parameters.
func EncodeStartup(params map[string]string) []byte {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, ProtocolVersion)
	for k, v := range params {
		body = append(body, k...)
		body = append(body, 0)
		body = append(body, v...)
		body = append(body, 0)
	}
	body = append(body, 0)
	return prependLen(body)
}

// EncodeSSLRequest builds the TLS negotiation probe sent to an upstream.
func EncodeSSLRequest() []byte {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, SSLRequestCode)
	return prependLen(body)
}

// EncodeCancel builds a cancel-request packet to forward upstream.
func EncodeCancel(pid, secret uint32) []byte {
	var body []byte
	body = binary.BigEndian.AppendUint32(body, CancelRequestCode)
	body = binary.BigEndian.AppendUint32(body, pid)
	body = binary.BigEndian.AppendUint32(body, secret)
	return prependLen(body)
}

func prependLen(body []byte) []byte {
	buf := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(buf, uint32(len(buf)))
	copy(buf[4:], body)
	return buf
}

// AuthReq is a parsed authentication request ('R') from a server.
type AuthReq struct {
	Code       uint32
	Salt       [4]byte  // AuthMD5Password
	Mechanisms []string // AuthSASL
	Data       []byte   // AuthSASLContinue / AuthSASLFinal
}

// ParseAuth decodes an 'R' message payload.
func ParseAuth(payload []byte) (*AuthReq, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("authentication message too short")
	}
	a := &AuthReq{Code: binary.BigEndian.Uint32(payload[:4])}
	rest := payload[4:]
	switch a.Code {
	case AuthMD5Password:
		if len(rest) != 4 {
			return nil, fmt.Errorf("invalid md5 salt length %d", len(rest))
		}
		copy(a.Salt[:], rest)
	case AuthSASL:
		for len(rest) > 0 && rest[0] != 0 {
			mech, r, err := cutCString(rest)
			if err != nil {
				return nil, err
			}
			a.Mechanisms = append(a.Mechanisms, mech)
			rest = r
		}
	case AuthSASLContinue, AuthSASLFinal:
		a.Data = rest
	}
	return a, nil
}

// AuthOKMsg is the AuthenticationOk message mirrored to the client once the
// upstream accepts the real credential.
func AuthOKMsg() Msg {
	return authMsg(AuthOK)
}

// CleartextPasswordRequest asks the client for its (fake) password.
func CleartextPasswordRequest() Msg {
	return authMsg(AuthCleartextPassword)
}

func authMsg(code uint32) Msg {
	return Msg{Type: MsgAuth, Payload: binary.BigEndian.AppendUint32(nil, code)}
}

// PasswordMessage builds a cleartext password response ('p').
func PasswordMessage(pw string) Msg {
	return Msg{Type: MsgPassword, Payload: append([]byte(pw), 0)}
}

// SASLInitialResponse builds the first client message of a SASL exchange.
func SASLInitialResponse(mechanism string, data []byte) Msg {
	var p []byte
	p = append(p, mechanism...)
	p = append(p, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(data)))
	p = append(p, data...)
	return Msg{Type: MsgPassword, Payload: p}
}

// SASLResponse builds a continuation message of a SASL exchange.
func SASLResponse(data []byte) Msg {
	return Msg{Type: MsgPassword, Payload: data}
}

// CString extracts a NUL-terminated string from a payload (e.g. a client
// PasswordMessage).
func CString(payload []byte) string {
	s, _, err := cutCString(payload)
	if err != nil {
		return string(payload)
	}
	return s
}

// ErrorResponseMsg builds a FATAL ErrorResponse with the given SQLSTATE.
func ErrorResponseMsg(sqlstate, message string) Msg {
	var p []byte
	for _, f := range [][2]string{
		{"S", "FATAL"}, {"V", "FATAL"}, {"C", sqlstate}, {"M", message},
	} {
		p = append(p, f[0]...)
		p = append(p, f[1]...)
		p = append(p, 0)
	}
	p = append(p, 0)
	return Msg{Type: MsgError, Payload: p}
}

// ErrorCode extracts the SQLSTATE from an ErrorResponse payload. Only the
// code is ever surfaced from upstream errors: their message text can embed
// real identifiers (usernames, database names) that must not reach anything
// the agent sees.
func ErrorCode(payload []byte) string {
	b := payload
	for len(b) > 0 && b[0] != 0 {
		field := b[0]
		val, rest, err := cutCString(b[1:])
		if err != nil {
			break
		}
		if field == 'C' {
			return val
		}
		b = rest
	}
	return "XX000" // internal_error: payload had no SQLSTATE field
}
