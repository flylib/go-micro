package conformance

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flylib/go-micro/auth"
	"github.com/flylib/go-micro/auth/jwt/token"
	merr "github.com/flylib/go-micro/errors"
	"golang.org/x/net/http2"
	pb "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/protobuf/encoding/protojson"
)

// HTTP/JSON entry cases (SPEC 2.1, 14). They run only when the gateway
// offers the entry, and each runs over HTTP/1.1 and over h2c.

type httpProto struct {
	name   string
	client *http.Client
	want   string // resp.Proto, proving which protocol was used
}

func httpProtos() []httpProto {
	h2c := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	protos := []httpProto{{"HTTP/1.1", &http.Client{Timeout: callTimeout, Transport: &http.Transport{}}, "HTTP/1.1"}}
	// HTTP/1.1-only entries (e.g. the Go gateway on fasthttp) skip h2c
	if os.Getenv("GATEWAY_HTTP_H2C") != "false" {
		protos = append(protos, httpProto{"h2c", &http.Client{Timeout: callTimeout, Transport: h2c}, "HTTP/2.0"})
	}
	return protos
}

// httpSuite returns the shared fixture, skipping without an HTTP entry.
func httpSuite(t *testing.T) *suite {
	t.Helper()
	if loadConfig().httpGateway == "" {
		t.Skip("GATEWAY_HTTP_ADDR not set: HTTP/JSON entry cases skipped")
	}
	return gatewaySuite(t)
}

type httpReply struct {
	status int
	header http.Header
	body   []byte
}

// post calls the HTTP entry. hdrs are header name/value pairs.
func (s *suite) post(t *testing.T, p httpProto, method, path, ctype, body string, hdrs ...string) httpReply {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+s.cfg.httpGateway+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for i := 0; i+1 < len(hdrs); i += 2 {
		req.Header.Set(hdrs[i], hdrs[i+1])
	}
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.Proto != p.want {
		t.Errorf("reply over %s, want %s", resp.Proto, p.want)
	}
	b, _ := io.ReadAll(resp.Body)
	return httpReply{status: resp.StatusCode, header: resp.Header, body: b}
}

func apiPath(service, handler, method string) string {
	return fmt.Sprintf("/api/%s/%s/%s", service, handler, method)
}

// echoOf decodes a 200 reply: the SimpleResponse JSON the service wrote.
func echoOf(t *testing.T, r httpReply) echo {
	t.Helper()
	if r.status != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", r.status, r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type %q, want application/json", ct)
	}
	var rsp pb.SimpleResponse
	if err := protojson.Unmarshal(r.body, &rsp); err != nil {
		t.Fatalf("reply is not a SimpleResponse: %v: %s", err, r.body)
	}
	e, err := decodeEcho(rsp.GetPayload())
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// microErrorOf decodes an error reply: a go-micro error with the status.
func microErrorOf(t *testing.T, r httpReply, status int) *merr.Error {
	t.Helper()
	if r.status != status {
		t.Errorf("status %d, want %d: %s", r.status, status, r.body)
	}
	e := &merr.Error{}
	if err := json.Unmarshal(r.body, e); err != nil || e.Code == 0 {
		t.Fatalf("body is not a go-micro error: %s", r.body)
	}
	if e.Code != int32(status) {
		t.Errorf("error code %d, want %d", e.Code, status)
	}
	return e
}

// awaitHTTP polls path until it answers 200, within the propagation window.
func (s *suite) awaitHTTP(t *testing.T, p httpProto, path string) {
	t.Helper()
	deadline := time.Now().Add(propagation + grace)
	for {
		req, _ := http.NewRequest(http.MethodPost, "http://"+s.cfg.httpGateway+path, bytes.NewReader([]byte("{}")))
		if resp, err := p.client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never answered 200 within %s", path, propagation+grace)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestJ1_HTTPCall(t *testing.T) {
	s := httpSuite(t)
	s.reset(t)
	svc := s.svc("j1")
	s.start(t, svc)
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			path := apiPath(svc, "TestService", "UnaryCall")
			s.awaitHTTP(t, p, path)
			if e := echoOf(t, s.post(t, p, http.MethodPost, path, "application/json", "{}")); e.Service != svc {
				t.Errorf("answered by %q, want %q", e.Service, svc)
			}
		})
	}
}

func TestJ2_HTTPRoutesApply(t *testing.T) {
	s := httpSuite(t)
	a := s.svc("j2a")
	s.start(t, a)
	y := s.svc("j2y") // path package with no nodes of its own
	s.apply(t, rules{"routes": []route{
		{"name": "j2", "match": map[string]any{"method": path(y, "TestService", "Echo")}, "upstream": map[string]any{"service": a}},
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			p2 := apiPath(y, "TestService", "Echo")
			s.awaitHTTP(t, p, p2)
			if e := echoOf(t, s.post(t, p, http.MethodPost, p2, "", "")); e.Service != a {
				t.Errorf("answered by %q, want %q", e.Service, a)
			}
		})
	}
}

func TestJ3_HTTPServiceError(t *testing.T) {
	s := httpSuite(t)
	s.reset(t)
	svc := s.svc("j3")
	s.start(t, svc)
	path := apiPath(svc, "TestService", "UnaryCall")
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			s.awaitHTTP(t, p, path)
			r := s.post(t, p, http.MethodPost, path, "application/json", `{"response_status":{"code":400,"message":"name is required"}}`)
			e := microErrorOf(t, r, http.StatusBadRequest)
			if e.Id != svc || e.Detail != "name is required" {
				t.Errorf("service error altered: %s", r.body)
			}
		})
	}
}

func TestJ4_HTTPNoNodes(t *testing.T) {
	s := httpSuite(t)
	s.reset(t)
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.post(t, p, http.MethodPost, apiPath(s.svc("j4-nonode"), "TestService", "UnaryCall"), "application/json", "{}")
			if e := microErrorOf(t, r, http.StatusServiceUnavailable); e.Id != "micro.gateway" {
				t.Errorf("id %q, want micro.gateway", e.Id)
			}
		})
	}
}

func TestJ5_HTTPAuthAndHeaders(t *testing.T) {
	s := httpSuite(t)
	svc := s.svc("j5")
	s.start(t, svc)
	priv, pub, _ := rsaKeys(t)
	s.apply(t, rules{"routes": []route{jwtRoute("j5", svc, pub)}})
	tok, err := token.New(token.WithPrivateKey(priv), token.WithPublicKey(pub)).
		Generate(&auth.Account{ID: "user-7"}, token.WithExpiry(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	path := apiPath(svc, "TestService", "UnaryCall")
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.post(t, p, http.MethodPost, path, "application/json", "{}")
			if e := microErrorOf(t, r, http.StatusUnauthorized); e.Id != "micro.gateway" {
				t.Errorf("id %q, want micro.gateway", e.Id)
			}

			r = s.post(t, p, http.MethodPost, path, "application/json", "{}",
				"Authorization", "Bearer "+tok.Token,
				"X-Tenant", "t1",
				"Micro-Gateway-Account", "spoofed")
			e := echoOf(t, r)
			if v, _ := e.header("micro-gateway-account"); v != "user-7" {
				t.Errorf("micro-gateway-account = %q, want user-7", v)
			}
			if v, _ := e.header("x-tenant"); v != "t1" {
				t.Errorf("client header x-tenant = %q, want t1", v)
			}
			if tp, _ := e.header("traceparent"); !traceparentRe.MatchString(tp) {
				t.Errorf("traceparent %q not generated", tp)
			}
		})
	}
}

func TestJ6_HTTPRefusals(t *testing.T) {
	s := httpSuite(t)
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			microErrorOf(t, s.post(t, p, http.MethodGet, apiPath("svc", "TestService", "UnaryCall"), "", ""), http.StatusMethodNotAllowed)
			microErrorOf(t, s.post(t, p, http.MethodPost, "/api/svc/TestService", "", ""), http.StatusNotFound)
			microErrorOf(t, s.post(t, p, http.MethodPost, apiPath("svc", "TestService", "UnaryCall"), "text/plain", "hi"), http.StatusUnsupportedMediaType)
		})
	}
}
