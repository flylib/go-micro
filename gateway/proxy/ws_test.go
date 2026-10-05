package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/flylib/go-micro/broker"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/gateway/push"
	"github.com/flylib/go-micro/registry"
)

// echoUpstream is a gRPC server for any method:
//
//	/echo.Echo/Call    replies {"req": <request>, "md": {<selected metadata>}}
//	/echo.Echo/Stream  echoes every message until the client stops sending
//	/echo.Echo/Fail    fails with a go-micro BadRequest
//	/echo.Echo/Hang    waits until the call is canceled
func echoUpstream(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.ForceServerCodec(rawCodec{name: "json"}), grpc.UnknownServiceHandler(func(_ any, s grpc.ServerStream) error {
		method, _ := grpc.MethodFromServerStream(s)
		switch method {
		case "/echo.Echo/Call":
			f := &frame{}
			if err := s.RecvMsg(f); err != nil {
				return err
			}
			md, _ := metadata.FromIncomingContext(s.Context())
			pick := map[string]string{}
			for _, k := range []string{"x-client", "x-call", "user-id", "micro-gateway-account"} {
				if v := md.Get(k); len(v) > 0 {
					pick[k] = v[0]
				}
			}
			mdj, _ := json.Marshal(pick)
			return s.SendMsg(&frame{data: []byte(`{"req":` + string(f.data) + `,"md":` + string(mdj) + `}`)})
		case "/echo.Echo/Stream":
			for {
				f := &frame{}
				err := s.RecvMsg(f)
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				if err := s.SendMsg(f); err != nil {
					return err
				}
			}
		case "/echo.Echo/Fail":
			return status.Error(codes.InvalidArgument, merr.BadRequest("echo", "bad input").Error())
		case "/echo.Echo/Hang":
			<-s.Context().Done()
			return s.Context().Err()
		}
		return status.Error(codes.Unimplemented, method)
	}))
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(srv.Stop)
	return l.Addr().String()
}

// wsGateway starts a gateway with the rules doc (which gets version: 1)
// and returns its HTTP entry URL.
func wsGateway(t *testing.T, doc string, b broker.Broker) (string, *Gateway) {
	t.Helper()
	reg := registry.NewMemoryRegistry()
	if err := reg.Register(&registry.Service{Name: "echo", Nodes: []*registry.Node{
		{Id: "echo-1", Address: echoUpstream(t), Metadata: map[string]string{"protocol": "grpc"}},
	}}); err != nil {
		t.Fatal(err)
	}
	opts := []Option{Registry(reg)}
	if b != nil {
		opts = append(opts, Broker(b))
	}
	gw, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	gw.rules.Store(mustRules(t, "version: 1\n"+doc))
	srv := httptest.NewServer(gw.HTTPHandler())
	t.Cleanup(func() { gw.Stop(); srv.Close() })
	return "ws" + strings.TrimPrefix(srv.URL, "http"), gw
}

type wsClient struct {
	t    *testing.T
	ws   *websocket.Conn
	msgs chan []byte
	err  chan error // the read error that ended the connection
}

func dialWS(t *testing.T, url string, h http.Header) *wsClient {
	t.Helper()
	ws, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{"micro.v1"}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial %s: %v (HTTP %d)", url, err, code)
	}
	if ws.Subprotocol() != "micro.v1" {
		t.Errorf("subprotocol %q", ws.Subprotocol())
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return newWSClient(t, ws)
}

// newWSClient reads in the background: canceling a Read closes a
// coder/websocket connection, so reads cannot time out themselves.
func newWSClient(t *testing.T, ws *websocket.Conn) *wsClient {
	c := &wsClient{t: t, ws: ws, msgs: make(chan []byte, 64), err: make(chan error, 1)}
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
	return c
}

func (c *wsClient) send(m string) {
	c.t.Helper()
	if err := c.ws.Write(context.Background(), websocket.MessageText, []byte(m)); err != nil {
		c.t.Fatal(err)
	}
}

type wsMsg struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Topic string          `json:"topic"`
	Body  json.RawMessage `json:"body"`
	Error *merr.Error     `json:"error"`
}

func (c *wsClient) recv() wsMsg {
	c.t.Helper()
	var b []byte
	select {
	case b = <-c.msgs:
	case err := <-c.err:
		c.t.Fatalf("read: %v", err)
	case <-time.After(5 * time.Second):
		c.t.Fatal("no message within 5s")
	}
	var m wsMsg
	if err := json.Unmarshal(b, &m); err != nil {
		c.t.Fatalf("message %s: %v", b, err)
	}
	return m
}

// quiet asserts nothing arrives for a short while.
func (c *wsClient) quiet() {
	c.t.Helper()
	select {
	case b := <-c.msgs:
		c.t.Fatalf("unexpected message %s", b)
	case <-time.After(200 * time.Millisecond):
	}
}

// closed waits for the connection to end and returns its close status.
func (c *wsClient) closed() websocket.StatusCode {
	c.t.Helper()
	select {
	case err := <-c.err:
		return websocket.CloseStatus(err)
	case <-time.After(5 * time.Second):
		c.t.Fatal("connection not closed within 5s")
	}
	return 0
}

func TestWSOffWithoutSection(t *testing.T) {
	url, _ := wsGateway(t, "", nil)
	_, resp, err := websocket.Dial(context.Background(), url+"/ws", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %v %v", resp, err)
	}
}

func TestWSCalls(t *testing.T) {
	url, _ := wsGateway(t, "websocket: {}", nil)
	c := dialWS(t, url+"/ws", http.Header{"X-Client": {"c1"}})

	c.send(`{"id":"a","type":"call","method":"/echo.Echo/Call","body":{"n":1}}`)
	c.send(`{"id":"b","type":"call","method":"/echo.Echo/Call","body":{"n":2},"metadata":{"X-Call":"v"}}`)
	got := map[string]string{}
	for i := 0; i < 2; i++ {
		m := c.recv()
		if m.Type != "reply" {
			t.Fatalf("got %+v", m)
		}
		got[m.ID] = string(m.Body)
	}
	if got["a"] != `{"req":{"n":1},"md":{"x-client":"c1"}}` || got["b"] != `{"req":{"n":2},"md":{"x-call":"v","x-client":"c1"}}` {
		t.Fatalf("replies %v", got)
	}

	c.send(`{"id":"e","type":"call","method":"/echo.Echo/Fail"}`)
	if m := c.recv(); m.Type != "error" || m.ID != "e" || m.Error.Code != 400 || m.Error.Id != "echo" {
		t.Fatalf("service error: %+v %+v", m, m.Error)
	}
	c.send(`{"id":"n","type":"call","method":"/nope.Nope/Call"}`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 503 || m.Error.Id != errorID {
		t.Fatalf("no nodes: %+v %+v", m, m.Error)
	}
}

func TestWSStreams(t *testing.T) {
	url, _ := wsGateway(t, "websocket: {}", nil)
	c := dialWS(t, url+"/ws", nil)

	// one message and close_send: a server-streaming shape
	c.send(`{"id":"s1","type":"stream","method":"/echo.Echo/Stream","body":{"x":1},"close_send":true}`)
	if m := c.recv(); m.Type != "message" || string(m.Body) != `{"x":1}` {
		t.Fatalf("got %+v", m)
	}
	if m := c.recv(); m.Type != "end" || m.ID != "s1" {
		t.Fatalf("got %+v", m)
	}

	// bidirectional
	c.send(`{"id":"s2","type":"stream","method":"/echo.Echo/Stream"}`)
	for i, body := range []string{`{"a":1}`, `"two"`} {
		c.send(`{"id":"s2","type":"send","body":` + body + `}`)
		if m := c.recv(); m.Type != "message" || string(m.Body) != body {
			t.Fatalf("message %d: %+v", i, m)
		}
	}
	c.send(`{"id":"s2","type":"close_send"}`)
	if m := c.recv(); m.Type != "end" {
		t.Fatalf("got %+v", m)
	}
	c.send(`{"id":"s2","type":"send","body":{}}`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 400 {
		t.Fatalf("send on a finished stream: %+v", m)
	}
}

func TestWSProtocolErrors(t *testing.T) {
	url, _ := wsGateway(t, "websocket: {max_calls: 1}", nil)
	c := dialWS(t, url+"/ws", nil)

	c.send(`{"id":"x","type":`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 400 {
		t.Fatalf("malformed: %+v", m)
	}
	c.send(`{"id":"x","type":"nonsense"}`)
	if m := c.recv(); m.Type != "error" || m.ID != "x" || m.Error.Code != 400 {
		t.Fatalf("unknown type: %+v", m)
	}
	c.send(`{"type":"call","method":"/echo.Echo/Call"}`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 400 {
		t.Fatalf("no id: %+v", m)
	}
	c.send(`{"id":"p","type":"ping"}`)
	if m := c.recv(); m.Type != "pong" || m.ID != "p" {
		t.Fatalf("ping: %+v", m)
	}

	// max_calls 1: a second open call is refused, then cancel frees it
	c.send(`{"id":"h","type":"call","method":"/echo.Echo/Hang"}`)
	time.Sleep(100 * time.Millisecond)
	c.send(`{"id":"h","type":"call","method":"/echo.Echo/Call"}`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 400 {
		t.Fatalf("duplicate id: %+v", m)
	}
	c.send(`{"id":"h2","type":"call","method":"/echo.Echo/Call"}`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 429 {
		t.Fatalf("over max_calls: %+v", m)
	}
	c.send(`{"id":"h","type":"cancel"}`)
	if m := c.recv(); m.Type != "error" || m.ID != "h" || m.Error.Code != 499 {
		t.Fatalf("cancel: %+v %+v", m, m.Error)
	}
	c.send(`{"id":"ok","type":"call","method":"/echo.Echo/Call"}`)
	if m := c.recv(); m.Type != "reply" {
		t.Fatalf("after cancel: %+v", m)
	}

	if err := c.ws.Write(context.Background(), websocket.MessageBinary, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if st := c.closed(); st != websocket.StatusUnsupportedData {
		t.Fatalf("binary frame: close status %v", st)
	}
}

func TestWSAuthAndClaims(t *testing.T) {
	s := newSigner(t)
	url, _ := wsGateway(t, `websocket:
  plugins:
    - name: jwt-auth
      config: {public_key: "`+s.pub+`", forward_claims: {user-id: sub}}
`, nil)

	_, resp, err := websocket.Dial(context.Background(), url+"/ws", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: want 401, got %v %v", resp, err)
	}

	tok := s.rs256(t, map[string]any{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	c := dialWS(t, url+"/ws?access_token="+tok, nil)
	c.send(`{"id":"a","type":"call","method":"/echo.Echo/Call","metadata":{"user-id":"forged"}}`)
	m := c.recv()
	var body struct {
		MD map[string]string `json:"md"`
	}
	if err := json.Unmarshal(m.Body, &body); err != nil {
		t.Fatalf("%+v: %v", m, err)
	}
	if body.MD["user-id"] != "u1" || body.MD["micro-gateway-account"] != "u1" {
		t.Fatalf("metadata at the service: %v", body.MD)
	}
}

func TestWSPushAndTopics(t *testing.T) {
	s := newSigner(t)
	b := broker.NewMemoryBroker()
	url, _ := wsGateway(t, `websocket:
  plugins:
    - name: jwt-auth
      config: {public_key: "`+s.pub+`"}
  topics: ["room.*", "user.{account}.>"]
`, b)
	token := func(sub string) http.Header {
		tok := s.rs256(t, map[string]any{"sub": sub, "exp": time.Now().Add(time.Hour).Unix()})
		return http.Header{"Authorization": {"Bearer " + tok}}
	}
	a1, a2, other := dialWS(t, url+"/ws", token("u1")), dialWS(t, url+"/ws", token("u1")), dialWS(t, url+"/ws", token("u2"))
	time.Sleep(100 * time.Millisecond) // connections join their account topic

	p := push.New(b)
	if err := p.ToUser(context.Background(), "u1", map[string]any{"hello": "u1"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []*wsClient{a1, a2} {
		if m := c.recv(); m.Type != "push" || string(m.Body) != `{"hello":"u1"}` {
			t.Fatalf("push: %+v", m)
		}
	}
	other.quiet()

	a1.send(`{"id":"1","type":"subscribe","topic":"room.42"}`)
	if m := a1.recv(); m.Type != "subscribed" || m.Topic != "room.42" {
		t.Fatalf("subscribe: %+v", m)
	}
	a1.send(`{"id":"2","type":"subscribe","topic":"secret.1"}`)
	if m := a1.recv(); m.Type != "error" || m.Error.Code != 403 {
		t.Fatalf("not allowed: %+v", m)
	}
	a1.send(`{"id":"3","type":"subscribe","topic":"user.u1.inbox"}`)
	if m := a1.recv(); m.Type != "subscribed" {
		t.Fatalf("own account topic: %+v", m)
	}
	other.send(`{"id":"4","type":"subscribe","topic":"user.u1.inbox"}`)
	if m := other.recv(); m.Type != "error" || m.Error.Code != 403 {
		t.Fatalf("another account's topic: %+v", m)
	}
	a1.send(`{"id":"5","type":"subscribe","topic":"bad topic"}`)
	if m := a1.recv(); m.Type != "error" || m.Error.Code != 400 {
		t.Fatalf("invalid topic: %+v", m)
	}

	if err := p.ToTopic(context.Background(), "room.42", "hi"); err != nil {
		t.Fatal(err)
	}
	if m := a1.recv(); m.Type != "event" || m.Topic != "room.42" || string(m.Body) != `"hi"` {
		t.Fatalf("event: %+v", m)
	}
	a2.quiet()

	a1.send(`{"id":"6","type":"unsubscribe","topic":"room.42"}`)
	if m := a1.recv(); m.Type != "unsubscribed" {
		t.Fatalf("unsubscribe: %+v", m)
	}
	if err := p.ToTopic(context.Background(), "room.42", "again"); err != nil {
		t.Fatal(err)
	}
	a1.quiet()
}

func TestWSTopicsNeedBroker(t *testing.T) {
	url, _ := wsGateway(t, `websocket: {topics: [">"]}`, nil)
	c := dialWS(t, url+"/ws", nil)
	c.send(`{"id":"1","type":"subscribe","topic":"room.1"}`)
	if m := c.recv(); m.Type != "error" || m.Error.Code != 503 {
		t.Fatalf("without a broker: %+v", m)
	}
}

func TestTopicPattern(t *testing.T) {
	cases := []struct {
		pattern, topic, account string
		want                    bool
	}{
		{"room.*", "room.1", "", true},
		{"room.*", "room.1.x", "", false},
		{"room.*", "room", "", false},
		{"room.>", "room.1.x", "", true},
		{"room.>", "room", "", false},
		{">", "anything.at.all", "", true},
		{"user.{account}.>", "user.u1.inbox", "u1", true},
		{"user.{account}.>", "user.u2.inbox", "u1", false},
		{"user.{account}.>", "user..inbox", "", false},
		{"user.{account}", "user.a.b", "a.b", false},
		{"chat-{account}", "chat-u1", "u1", true},
	}
	for _, tc := range cases {
		p := topicPattern(strings.Split(tc.pattern, "."))
		if got := p.match(strings.Split(tc.topic, "."), tc.account); got != tc.want {
			t.Errorf("%q ~ %q (account %q) = %v", tc.pattern, tc.topic, tc.account, got)
		}
	}
}

func TestWSCloses(t *testing.T) {
	url, gw := wsGateway(t, "websocket: {}", nil)

	big := dialWS(t, url+"/ws", nil)
	big.ws.SetReadLimit(-1)
	if err := big.ws.Write(context.Background(), websocket.MessageText, []byte(`{"id":"x","type":"call","body":"`+strings.Repeat("a", wsMaxMessage)+`"}`)); err != nil {
		t.Fatal(err)
	}
	if st := big.closed(); st != websocket.StatusMessageTooBig {
		t.Fatalf("oversized message: close status %v", st)
	}

	c := dialWS(t, url+"/ws", nil)
	c.send(`{"id":"p","type":"ping"}`)
	c.recv()
	gw.Stop()
	if st := c.closed(); st != websocket.StatusGoingAway {
		t.Fatalf("gateway stop: close status %v", st)
	}
}
