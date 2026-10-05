package conformance

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/flylib/go-micro/auth"
	"github.com/flylib/go-micro/auth/jwt/token"
)

// HTTP rule cases (SPEC 2.2, 14): REST calls mapped by http_rules. They
// run with the HTTP/JSON entry cases, over HTTP/1.1 and h2c.

// requestOf returns the request message the service decoded.
func requestOf(t *testing.T, e echo) map[string]any {
	t.Helper()
	var m map[string]any
	if len(e.Request) == 0 {
		return map[string]any{}
	}
	if err := json.Unmarshal(e.Request, &m); err != nil {
		t.Fatalf("echoed request is not JSON: %s", e.Request)
	}
	return m
}

func httpRule(method, tmpl, target string, extra map[string]any) map[string]any {
	r := map[string]any{"method": method, "path": tmpl, "target": target}
	for k, v := range extra {
		r[k] = v
	}
	return r
}

// get calls the HTTP entry, waiting out the rules propagation first.
func (s *suite) restCall(t *testing.T, p httpProto, method, path, body string, hdrs ...string) httpReply {
	t.Helper()
	ctype := ""
	if body != "" {
		ctype = "application/json"
	}
	deadline := time.Now().Add(propagation + grace)
	for {
		r := s.post(t, p, method, path, ctype, body, hdrs...)
		// 404 or 503 until the new rules and nodes are live
		if (r.status != http.StatusNotFound && r.status != http.StatusServiceUnavailable) || time.Now().After(deadline) {
			return r
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestT1_PathAndQueryParameters(t *testing.T) {
	s := httpSuite(t)
	svc := s.svc("t1")
	s.start(t, svc)
	s.apply(t, rules{"http_rules": []any{
		httpRule("GET", "/v1/"+svc+"/size/{response_size}", path(svc, "TestService", "UnaryCall"),
			// a params map is an allow-list: response_status.message must be in it
			map[string]any{"params": map[string]any{"fill_username": "bool", "response_status.message": "string"}}),
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.restCall(t, p, http.MethodGet, "/v1/"+svc+"/size/12?fill_username=true&response_status.message=hi&not_listed=1", "")
			req := requestOf(t, echoOf(t, r))
			if req["response_size"] != float64(12) {
				t.Errorf("response_size = %v, want 12 (path variable)", req["response_size"])
			}
			if req["fill_username"] != true {
				t.Errorf("fill_username = %v, want true (bool query parameter)", req["fill_username"])
			}
			if st, _ := req["response_status"].(map[string]any); st == nil || st["message"] != "hi" {
				t.Errorf("response_status = %v, want message hi (nested query parameter)", req["response_status"])
			}
			if _, set := req["not_listed"]; set {
				t.Error("a parameter missing from params reached the service")
			}
		})
	}
}

func TestT2_StarBody(t *testing.T) {
	s := httpSuite(t)
	svc := s.svc("t2")
	s.start(t, svc)
	s.apply(t, rules{"http_rules": []any{
		httpRule("POST", "/v1/"+svc+"/star/{response_size}", path(svc, "TestService", "UnaryCall"),
			map[string]any{"body": "*"}),
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.restCall(t, p, http.MethodPost, "/v1/"+svc+"/star/33?fill_oauth_scope=true", `{"response_size":1,"fill_username":true}`)
			req := requestOf(t, echoOf(t, r))
			if req["response_size"] != float64(33) {
				t.Errorf("response_size = %v, want 33: the path variable overrides the body", req["response_size"])
			}
			if req["fill_username"] != true {
				t.Errorf("fill_username = %v, want true from the body", req["fill_username"])
			}
			if _, set := req["fill_oauth_scope"]; set {
				t.Error("query parameter used with body \"*\"")
			}
		})
	}
}

func TestT3_FieldBody(t *testing.T) {
	s := httpSuite(t)
	svc := s.svc("t3")
	s.start(t, svc)
	s.apply(t, rules{"http_rules": []any{
		httpRule("POST", "/v1/"+svc+"/payload", path(svc, "TestService", "UnaryCall"), map[string]any{"body": "payload"}),
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.restCall(t, p, http.MethodPost, "/v1/"+svc+"/payload?response_size=5", `{"body":"aGk="}`)
			req := requestOf(t, echoOf(t, r))
			if pl, _ := req["payload"].(map[string]any); pl == nil || pl["body"] != "aGk=" {
				t.Errorf("payload = %v, want the request body", req["payload"])
			}
			if req["response_size"] != float64(5) {
				t.Errorf("response_size = %v, want 5 from the query", req["response_size"])
			}
		})
	}
}

func TestT4_ResponseBody(t *testing.T) {
	s := httpSuite(t)
	svc := s.svc("t4")
	s.start(t, svc)
	s.apply(t, rules{"http_rules": []any{
		httpRule("GET", "/v1/"+svc+"/only-payload", path(svc, "TestService", "UnaryCall"),
			map[string]any{"response_body": "payload"}),
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.restCall(t, p, http.MethodGet, "/v1/"+svc+"/only-payload", "")
			if r.status != http.StatusOK {
				t.Fatalf("status %d: %s", r.status, r.body)
			}
			var pl map[string]string
			if err := json.Unmarshal(r.body, &pl); err != nil || len(pl) != 1 {
				t.Fatalf("body is not the payload field alone: %s", r.body)
			}
			raw, _ := base64.StdEncoding.DecodeString(pl["body"])
			var e echo
			if json.Unmarshal(raw, &e) != nil || e.Service != svc {
				t.Errorf("payload does not hold the echo from %s: %s", svc, r.body)
			}
		})
	}
}

func TestT5_RuleSpecificity(t *testing.T) {
	s := httpSuite(t)
	a, b, c := s.svc("t5a"), s.svc("t5b"), s.svc("t5c")
	for _, name := range []string{a, b, c} {
		s.start(t, name)
	}
	base := "/v1/" + s.svc("t5")
	// listed least specific first: order must not decide
	s.apply(t, rules{"http_rules": []any{
		httpRule("GET", base+"/**", path(c, "TestService", "UnaryCall"), nil),
		httpRule("GET", base+"/items/{response_size}", path(b, "TestService", "UnaryCall"), nil),
		httpRule("GET", base+"/items/special", path(a, "TestService", "UnaryCall"), nil),
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			for path, want := range map[string]string{
				base + "/items/special": a,
				base + "/items/7":       b,
				base + "/other/x/y":     c,
			} {
				if e := echoOf(t, s.restCall(t, p, http.MethodGet, path, "")); e.Service != want {
					t.Errorf("%s answered by %s, want %s", path, e.Service, want)
				}
			}
		})
	}
}

func TestT6_RuleRefusals(t *testing.T) {
	s := httpSuite(t)
	svc := s.svc("t6")
	s.start(t, svc)
	s.apply(t, rules{"http_rules": []any{
		httpRule("GET", "/v1/"+svc+"/flag", path(svc, "TestService", "UnaryCall"),
			map[string]any{"params": map[string]any{"fill_username": "bool"}}),
	}})
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			s.awaitHTTPGet(t, p, "/v1/"+svc+"/flag")
			if e := microErrorOf(t, s.post(t, p, http.MethodGet, "/v1/"+svc+"/flag?fill_username=yes", "", ""), http.StatusBadRequest); e.Id != "micro.gateway" {
				t.Errorf("bad bool: id %q, want micro.gateway", e.Id)
			}
			if e := microErrorOf(t, s.post(t, p, http.MethodGet, "/nomatch/"+svc, "", ""), http.StatusNotFound); e.Id != "micro.gateway" {
				t.Errorf("no rule: id %q, want micro.gateway", e.Id)
			}
		})
	}
}

func TestT7_ForwardClaims(t *testing.T) {
	s := httpSuite(t)
	svc, open := s.svc("t7"), s.svc("t7-open")
	s.start(t, svc)
	s.start(t, open)
	priv, pub, _ := rsaKeys(t)
	jr := jwtRoute("t7", svc, pub)
	jr["plugins"].([]any)[0].(map[string]any)["config"].(map[string]any)["forward_claims"] = map[string]string{"user-id": "sub"}
	s.apply(t, rules{
		"routes":     []route{jr},
		"http_rules": []any{httpRule("GET", "/v1/"+svc+"/me", path(svc, "TestService", "UnaryCall"), nil)},
	})
	tok, err := token.New(token.WithPrivateKey(priv), token.WithPublicKey(pub)).
		Generate(&auth.Account{ID: "user-99"}, token.WithExpiry(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range httpProtos() {
		t.Run(p.name, func(t *testing.T) {
			r := s.restCall(t, p, http.MethodGet, "/v1/"+svc+"/me", "", "Authorization", "Bearer "+tok.Token, "User-Id", "spoofed")
			if v, _ := echoOf(t, r).header("user-id"); v != "user-99" {
				t.Errorf("user-id = %q, want the token's sub user-99", v)
			}
			// a route without jwt-auth still drops a client-sent user-id
			e := echoOf(t, s.post(t, p, http.MethodPost, apiPath(open, "TestService", "UnaryCall"), "application/json", "{}", "User-Id", "spoofed"))
			if v, ok := e.header("user-id"); ok {
				t.Errorf("client-sent user-id reached %s: %q", open, v)
			}
		})
	}
}

// awaitHTTPGet polls a GET path until it answers 200.
func (s *suite) awaitHTTPGet(t *testing.T, p httpProto, path string) {
	t.Helper()
	deadline := time.Now().Add(propagation + grace)
	for {
		if r := s.post(t, p, http.MethodGet, path, "", ""); r.status == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s never answered 200 within %s", path, propagation+grace)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
