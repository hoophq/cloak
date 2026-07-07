package pgwire

import (
	"bytes"
	"testing"
)

func TestStartupRoundTrip(t *testing.T) {
	params := map[string]string{"user": "cloak", "database": "app", "application_name": "psql"}
	var buf bytes.Buffer
	buf.Write(EncodeStartup(params))

	payload, err := ReadStartup(&buf)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseStartup(payload)
	if err != nil {
		t.Fatal(err)
	}
	if s.Code != ProtocolVersion {
		t.Fatalf("code = %d, want %d", s.Code, ProtocolVersion)
	}
	for k, v := range params {
		if s.Params[k] != v {
			t.Errorf("param %q = %q, want %q", k, s.Params[k], v)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("%d bytes left unread", buf.Len())
	}
}

func TestSSLRequestAndCancel(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(EncodeSSLRequest())
	payload, err := ReadStartup(&buf)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseStartup(payload)
	if err != nil {
		t.Fatal(err)
	}
	if s.Code != SSLRequestCode {
		t.Fatalf("code = %d, want SSLRequest", s.Code)
	}

	buf.Reset()
	buf.Write(EncodeCancel(1234, 5678))
	payload, err = ReadStartup(&buf)
	if err != nil {
		t.Fatal(err)
	}
	s, err = ParseStartup(payload)
	if err != nil {
		t.Fatal(err)
	}
	if s.Code != CancelRequestCode || s.CancelPID != 1234 || s.CancelSecret != 5678 {
		t.Fatalf("cancel = %+v", s)
	}
}

func TestMsgRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMsg(&buf, PasswordMessage("tok123")); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMsg(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != MsgPassword || CString(m.Payload) != "tok123" {
		t.Fatalf("got %q payload %q", m.Type, m.Payload)
	}
	if buf.Len() != 0 {
		t.Errorf("%d bytes left unread", buf.Len())
	}
}

func TestParseAuth(t *testing.T) {
	// md5 with salt
	m := authMsg(AuthMD5Password)
	m.Payload = append(m.Payload, 1, 2, 3, 4)
	a, err := ParseAuth(m.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if a.Code != AuthMD5Password || a.Salt != [4]byte{1, 2, 3, 4} {
		t.Fatalf("md5 auth = %+v", a)
	}

	// SASL mechanism list
	m = authMsg(AuthSASL)
	m.Payload = append(m.Payload, "SCRAM-SHA-256-PLUS\x00SCRAM-SHA-256\x00\x00"...)
	a, err = ParseAuth(m.Payload)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"SCRAM-SHA-256-PLUS", "SCRAM-SHA-256"}
	if len(a.Mechanisms) != 2 || a.Mechanisms[0] != want[0] || a.Mechanisms[1] != want[1] {
		t.Fatalf("mechanisms = %v, want %v", a.Mechanisms, want)
	}
}

func TestErrorResponseRoundTrip(t *testing.T) {
	m := ErrorResponseMsg("28P01", "nope")
	if code := ErrorCode(m.Payload); code != "28P01" {
		t.Fatalf("ErrorCode = %q, want 28P01", code)
	}
	if code := ErrorCode([]byte{0}); code != "XX000" {
		t.Fatalf("ErrorCode on empty payload = %q, want XX000", code)
	}
}

func TestSASLInitialResponse(t *testing.T) {
	m := SASLInitialResponse("SCRAM-SHA-256", []byte("n,,n=u,r=abc"))
	if m.Type != MsgPassword {
		t.Fatalf("type = %q", m.Type)
	}
	if !bytes.HasPrefix(m.Payload, []byte("SCRAM-SHA-256\x00")) {
		t.Fatalf("payload = %q", m.Payload)
	}
	if !bytes.HasSuffix(m.Payload, []byte("n,,n=u,r=abc")) {
		t.Fatalf("payload = %q", m.Payload)
	}
}

func TestReadStartupRejectsBogusLength(t *testing.T) {
	if _, err := ReadStartup(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff})); err == nil {
		t.Fatal("expected error for oversized startup packet")
	}
}
