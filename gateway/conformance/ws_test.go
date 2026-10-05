package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	pb "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/flylib/go-micro/auth"
	"github.com/flylib/go-micro/auth/jwt/token"
	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/broker/nats"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/gateway/push"
)

// WebSocket entry cases (SPEC 2.3, 14). They run only when the gateway
// offers the entry, itself or by forwarding to a Go gateway.

func wsSuite(t *testing.T) *suite {
	t.Helper()
	if loadConfig().wsURL == "" {
		t.Skip("GATEWAY_WS_URL not set: WebSocket entry cases skipped")
	}
	return gatewaySuite(t)
}

var (
	brokerOnce sync.Once
	sharedPush *push.Pusher
	brokerErr  error
)

// pusher returns a Pusher on the gateway's broker, skipping without one.
func (s *suite) pusher(t *testing.T) *push.Pusher {
	t.Helper()
	if s.cfg.broker == "" {
		t.Skip("MICRO_BROKER not set: push cases skipped")
	}
	brokerOnce.Do(func() {
		if s.cfg.broker != "nats" {
			brokerErr = errUnsupportedBroker(s.cfg.broker)
			return
		}
		b := nats.NewNatsBroker(broker.Addrs(s.cfg.brokerAddrs...))
		if brokerErr = b.Connect(); brokerErr == nil {
			sharedPush = push.New(b)
		}
	})
	if brokerErr != nil {
		t.Fatalf("broker: %v", brokerErr)
	}
	return sharedPush
}

type errUnsupportedBroker string

func (e errUnsupportedBroker) Error() string { return "MICRO_BROKER must be nats, got " + string(e) }

type wsConn struct {
	t    *testing.T
	ws   *websocket.Conn
	msgs chan []byte
	err  chan error
}

type wsMsg struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Topic string          `json:"topic"`
	Body  json.RawMessage `json:"body"`
	Error *merr.Error     `json:"error"`
}

// dialWS connects with the query and headers; it returns the HTTP status
// when the upgrade is refused.
func (s *suite) dialWS(t *testing.T, query url.Values, hdr http.Header) (*wsConn, int) {
	t.Helper()
	u := s.cfg.wsURL
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		t.Fatalf("dial %s: %v", u, err)
	}
	ws.SetReadLimit(-1)
	c := &wsConn{t: t, ws: ws, msgs: make(chan []byte, 64), err: make(chan error, 1)}
	go func() {
		for {
			typ, b, err := ws.Read(context.Background())
			if err != nil {
				c.err <- err
				return
			}
			if typ == websocket.MessageText {
				c.msgs <- b
			}
		}
	}()
	t.Cleanup(func() { _ = ws.CloseNow() })
	return c, http.StatusSwitchingProtocols
}

// mustDial connects and fails the test when the upgrade is refused.
func (s *suite) mustDial(t *testing.T, query url.Values, hdr http.Header) *wsConn {
	t.Helper()
	c, status := s.dialWS(t, query, hdr)
	if c == nil {
		t.Fatalf("upgrade refused with HTTP %d", status)
	}
	return c
}

func (c *wsConn) send(v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

func (c *wsConn) recv() wsMsg {
	c.t.Helper()
	select {
	case b := <-c.msgs:
		var m wsMsg
		if err := json.Unmarshal(b, &m); err != nil {
			c.t.Fatalf("message is not JSON: %s", b)
		}
		return m
	case err := <-c.err:
		c.t.Fatalf("connection ended: %v", err)
	case <-time.After(callTimeout):
		c.t.Fatalf("no message within %s", callTimeout)
	}
	return wsMsg{}
}

func (c *wsConn) quiet(d time.Duration) {
	c.t.Helper()
	select {
	case b := <-c.msgs:
		c.t.Fatalf("unexpected message %s", b)
	case <-time.After(d):
	}
}

// echoOfMsg decodes a reply or stream message from the test service.
func echoOfMsg(t *testing.T, m wsMsg) echo {
	t.Helper()
	var rsp pb.SimpleResponse
	if err := protojson.Unmarshal(m.Body, &rsp); err != nil {
		t.Fatalf("body is not a TestService reply: %v: %s", err, m.Body)
	}
	e, err := decodeEcho(rsp.GetPayload())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func wsRules(extra map[string]any) map[string]any {
	ws := map[string]any{}
	for k, v := range extra {
		ws[k] = v
	}
	return ws
}

// awaitWS calls method over a fresh connection until it succeeds.
func (s *suite) awaitWS(t *testing.T, hdr http.Header, method string) {
	t.Helper()
	deadline := time.Now().Add(propagation + grace)
	for {
		c, _ := s.dialWS(t, nil, hdr)
		if c != nil {
			c.send(map[string]any{"id": "probe", "type": "call", "method": method, "body": map[string]any{}})
			if m := c.recv(); m.Type == "reply" {
				_ = c.ws.Close(websocket.StatusNormalClosure, "")
				return
			}
			_ = c.ws.CloseNow()
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never answered over WebSocket within %s", method, propagation+grace)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestW1_WSCalls(t *testing.T) {
	s := wsSuite(t)
	a, b := s.svc("w1a"), s.svc("w1b")
	s.start(t, a)
	s.start(t, b)
	s.apply(t, rules{"websocket": wsRules(nil)})
	s.awaitWS(t, nil, path(a, "TestService", "UnaryCall"))
	s.awaitWS(t, nil, path(b, "TestService", "UnaryCall"))

	c := s.mustDial(t, nil, nil)
	c.send(map[string]any{"id": "a", "type": "call", "method": path(a, "TestService", "UnaryCall"), "body": map[string]any{}})
	c.send(map[string]any{"id": "b", "type": "call", "method": path(b, "TestService", "UnaryCall"), "body": map[string]any{}})
	got := map[string]string{}
	for i := 0; i < 2; i++ {
		m := c.recv()
		if m.Type != "reply" {
			t.Fatalf("got %+v", m)
		}
		got[m.ID] = echoOfMsg(t, m).Service
	}
	if got["a"] != a || got["b"] != b {
		t.Errorf("replies by id: %v, want a=%s b=%s", got, a, b)
	}
}

func TestW2_WSErrors(t *testing.T) {
	s := wsSuite(t)
	svc := s.svc("w2")
	s.start(t, svc)
	s.apply(t, rules{"websocket": wsRules(nil)})
	s.awaitWS(t, nil, path(svc, "TestService", "UnaryCall"))

	c := s.mustDial(t, nil, nil)
	c.send(map[string]any{"id": "bad", "type": "call", "method": path(svc, "TestService", "UnaryCall"),
		"body": map[string]any{"response_status": map[string]any{"code": 400, "message": "bad input"}}})
	m := c.recv()
	if m.Type != "error" || m.ID != "bad" || m.Error == nil || m.Error.Code != 400 || m.Error.Id != svc || m.Error.Detail != "bad input" {
		t.Errorf("service error: %+v %+v", m, m.Error)
	}
	c.send(map[string]any{"id": "none", "type": "call", "method": path(s.svc("w2-missing"), "TestService", "UnaryCall")})
	if m := c.recv(); m.Type != "error" || m.Error == nil || m.Error.Code != 503 || m.Error.Id != "micro.gateway" {
		t.Errorf("no nodes: %+v %+v", m, m.Error)
	}
	c.send(map[string]any{"id": "ok", "type": "call", "method": path(svc, "TestService", "UnaryCall")})
	if m := c.recv(); m.Type != "reply" {
		t.Errorf("connection unusable after errors: %+v", m)
	}
}

func TestW3_WSStreams(t *testing.T) {
	s := wsSuite(t)
	svc := s.svc("w3")
	s.start(t, svc)
	s.apply(t, rules{"websocket": wsRules(nil)})
	s.awaitWS(t, nil, path(svc, "TestService", "UnaryCall"))

	c := s.mustDial(t, nil, nil)
	params := []any{map[string]any{}, map[string]any{}, map[string]any{}}
	c.send(map[string]any{"id": "out", "type": "stream", "method": path(svc, "TestService", "StreamingOutputCall"),
		"body": map[string]any{"response_parameters": params}, "close_send": true})
	for i := 0; i < 3; i++ {
		if m := c.recv(); m.Type != "message" || m.ID != "out" {
			t.Fatalf("server stream message %d: %+v", i, m)
		}
	}
	if m := c.recv(); m.Type != "end" || m.ID != "out" {
		t.Fatalf("server stream end: %+v", m)
	}

	c.send(map[string]any{"id": "dup", "type": "stream", "method": path(svc, "TestService", "FullDuplexCall")})
	for i := 0; i < 3; i++ {
		c.send(map[string]any{"id": "dup", "type": "send", "body": map[string]any{}})
		if m := c.recv(); m.Type != "message" || m.ID != "dup" || echoOfMsg(t, m).Service != svc {
			t.Fatalf("bidirectional reply %d: %+v", i, m)
		}
	}
	c.send(map[string]any{"id": "dup", "type": "close_send"})
	if m := c.recv(); m.Type != "end" {
		t.Fatalf("bidirectional end: %+v", m)
	}
}

// wsToken issues an auth/jwt token for sub.
func wsToken(t *testing.T, priv, pub, sub string) string {
	t.Helper()
	tok, err := token.New(token.WithPrivateKey(priv), token.WithPublicKey(pub)).
		Generate(&auth.Account{ID: sub}, token.WithExpiry(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return tok.Token
}

func jwtPlugin(pub string, forward map[string]string) []any {
	cfg := map[string]any{"public_key": pub}
	if forward != nil {
		cfg["forward_claims"] = forward
	}
	return []any{map[string]any{"name": "jwt-auth", "config": cfg}}
}

func TestW4_WSHandshake(t *testing.T) {
	s := wsSuite(t)
	s.reset(t)
	if c, status := s.dialWS(t, nil, nil); c != nil || status != http.StatusNotFound {
		t.Errorf("without a websocket section: upgraded=%v status %d, want 404", c != nil, status)
	}

	svc := s.svc("w4")
	s.start(t, svc)
	priv, pub, _ := rsaKeys(t)
	s.apply(t, rules{"websocket": wsRules(map[string]any{"plugins": jwtPlugin(pub, nil)})})
	tok := wsToken(t, priv, pub, "user-w4")
	s.awaitWS(t, http.Header{"Authorization": {"Bearer " + tok}}, path(svc, "TestService", "UnaryCall"))

	if c, status := s.dialWS(t, nil, nil); c != nil || status != http.StatusUnauthorized {
		t.Errorf("no token: upgraded=%v status %d, want 401", c != nil, status)
	}
	c := s.mustDial(t, url.Values{"access_token": {tok}}, nil)
	c.send(map[string]any{"id": "1", "type": "call", "method": path(svc, "TestService", "UnaryCall")})
	m := c.recv()
	if m.Type != "reply" {
		t.Fatalf("got %+v", m)
	}
	if v, _ := echoOfMsg(t, m).header("micro-gateway-account"); v != "user-w4" {
		t.Errorf("micro-gateway-account = %q, want the token's sub", v)
	}
}

func TestW5_WSPushToUser(t *testing.T) {
	s := wsSuite(t)
	p := s.pusher(t)
	svc := s.svc("w5")
	s.start(t, svc)
	priv, pub, _ := rsaKeys(t)
	s.apply(t, rules{"websocket": wsRules(map[string]any{"plugins": jwtPlugin(pub, nil)})})
	sub := "user-" + s.run + "-w5"
	tok := wsToken(t, priv, pub, sub)
	auth := http.Header{"Authorization": {"Bearer " + tok}}
	s.awaitWS(t, auth, path(svc, "TestService", "UnaryCall"))

	a1, a2 := s.mustDial(t, nil, auth), s.mustDial(t, nil, auth)
	other := s.mustDial(t, nil, http.Header{"Authorization": {"Bearer " + wsToken(t, priv, pub, sub+"-other")}})
	time.Sleep(500 * time.Millisecond) // the gateway subscribes for each account

	if err := p.ToUser(context.Background(), sub, map[string]any{"n": 1}); err != nil {
		t.Fatal(err)
	}
	for i, c := range []*wsConn{a1, a2} {
		if m := c.recv(); m.Type != "push" || string(m.Body) != `{"n":1}` {
			t.Errorf("connection %d: got %+v", i+1, m)
		}
	}
	other.quiet(500 * time.Millisecond)
}

func TestW6_WSTopics(t *testing.T) {
	s := wsSuite(t)
	p := s.pusher(t)
	svc := s.svc("w6")
	s.start(t, svc)
	priv, pub, _ := rsaKeys(t)
	room := "room-" + s.run
	s.apply(t, rules{"websocket": wsRules(map[string]any{
		"plugins": jwtPlugin(pub, nil),
		"topics":  []string{room + ".*", "user.{account}.>"},
	})})
	sub := "u" + s.run
	auth := http.Header{"Authorization": {"Bearer " + wsToken(t, priv, pub, sub)}}
	s.awaitWS(t, auth, path(svc, "TestService", "UnaryCall"))

	c := s.mustDial(t, nil, auth)
	c.send(map[string]any{"id": "1", "type": "subscribe", "topic": room + ".1"})
	if m := c.recv(); m.Type != "subscribed" || m.Topic != room+".1" {
		t.Fatalf("subscribe: %+v", m)
	}
	c.send(map[string]any{"id": "2", "type": "subscribe", "topic": "secret.1"})
	if m := c.recv(); m.Type != "error" || m.Error == nil || m.Error.Code != 403 {
		t.Errorf("topic not allowed: %+v", m)
	}
	c.send(map[string]any{"id": "3", "type": "subscribe", "topic": "user." + sub + ".inbox"})
	if m := c.recv(); m.Type != "subscribed" {
		t.Errorf("own account topic: %+v", m)
	}
	c.send(map[string]any{"id": "4", "type": "subscribe", "topic": "user.someone-else.inbox"})
	if m := c.recv(); m.Type != "error" || m.Error == nil || m.Error.Code != 403 {
		t.Errorf("another account's topic: %+v", m)
	}

	if err := p.ToTopic(context.Background(), room+".1", "hello"); err != nil {
		t.Fatal(err)
	}
	if m := c.recv(); m.Type != "event" || m.Topic != room+".1" || string(m.Body) != `"hello"` {
		t.Errorf("event: %+v", m)
	}
}

func TestW7_WSMetadata(t *testing.T) {
	s := wsSuite(t)
	svc := s.svc("w7")
	s.start(t, svc)
	priv, pub, _ := rsaKeys(t)
	s.apply(t, rules{"websocket": wsRules(map[string]any{"plugins": jwtPlugin(pub, map[string]string{"user-id": "sub"})})})
	hdr := http.Header{"Authorization": {"Bearer " + wsToken(t, priv, pub, "user-w7")}, "X-Client": {"c1"}, "User-Id": {"spoofed"}}
	s.awaitWS(t, hdr, path(svc, "TestService", "UnaryCall"))

	c := s.mustDial(t, nil, hdr)
	c.send(map[string]any{"id": "1", "type": "call", "method": path(svc, "TestService", "UnaryCall"),
		"metadata": map[string]string{"x-call": "v", "user-id": "spoofed-too"}})
	e := echoOfMsg(t, c.recv())
	for k, want := range map[string]string{"x-client": "c1", "x-call": "v", "user-id": "user-w7"} {
		if v, _ := e.header(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

func TestW8_WSMalformed(t *testing.T) {
	s := wsSuite(t)
	svc := s.svc("w8")
	s.start(t, svc)
	s.apply(t, rules{"websocket": wsRules(nil)})
	s.awaitWS(t, nil, path(svc, "TestService", "UnaryCall"))

	c := s.mustDial(t, nil, nil)
	if err := c.ws.Write(context.Background(), websocket.MessageText, []byte(`{"id":"x","type":`)); err != nil {
		t.Fatal(err)
	}
	if m := c.recv(); m.Type != "error" || m.Error == nil || m.Error.Code != 400 {
		t.Errorf("malformed message: %+v", m)
	}
	c.send(map[string]any{"id": "ok", "type": "call", "method": path(svc, "TestService", "UnaryCall")})
	if m := c.recv(); m.Type != "reply" {
		t.Errorf("connection unusable after a malformed message: %+v", m)
	}
	if err := c.ws.Write(context.Background(), websocket.MessageBinary, []byte{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-c.err:
		if st := websocket.CloseStatus(err); st != websocket.StatusUnsupportedData {
			t.Errorf("binary frame: close status %v, want 1003", st)
		}
	case <-time.After(callTimeout):
		t.Error("binary frame: connection not closed")
	}
}
