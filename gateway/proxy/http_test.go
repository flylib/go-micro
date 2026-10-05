package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newHTTPGateway(t *testing.T) http.Handler {
	t.Helper()
	gw, err := New(Registry(registry.NewMemoryRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Stop)
	return gw.HTTPHandler()
}

func do(t *testing.T, h http.Handler, method, path, ctype, body string) (int, *merr.Error) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s %s: content type %q", method, path, ct)
	}
	e := &merr.Error{}
	if err := json.Unmarshal(rec.Body.Bytes(), e); err != nil {
		t.Errorf("%s %s: body is not a go-micro error: %q", method, path, rec.Body.String())
	}
	return rec.Code, e
}

func TestHTTPRefusals(t *testing.T) {
	h := newHTTPGateway(t)
	cases := []struct {
		name, method, path, ctype, body string
		want                            int
	}{
		{"too few segments", http.MethodPost, "/api/svc/Handler", "", "", 404},
		{"not under /api", http.MethodPost, "/svc/Handler/Method", "", "", 404},
		{"GET", http.MethodGet, "/api/svc/Handler/Method", "", "", 405},
		{"text body", http.MethodPost, "/api/svc/Handler/Method", "text/plain", "x", 415},
		{"too large", http.MethodPost, "/api/svc/Handler/Method", "application/json", strings.Repeat("a", maxHTTPBody+1), 413},
		// passes the entry; the memory registry has no nodes
		{"no nodes", http.MethodPost, "/api/svc/Handler/Method", "application/json; charset=utf-8", "{}", 503},
	}
	for _, tc := range cases {
		code, e := do(t, h, tc.method, tc.path, tc.ctype, tc.body)
		if code != tc.want || e.Code != int32(tc.want) || e.Id != errorID {
			t.Errorf("%s: status %d body {id:%q code:%d}, want %d from %s", tc.name, code, e.Id, e.Code, tc.want, errorID)
		}
	}
}

func TestHTTPError(t *testing.T) {
	// a service's go-micro error passes through with its own code
	svcErr := status.Error(codes.InvalidArgument, merr.BadRequest("svc", "name is required").Error())
	code, body := httpError(svcErr, "svc")
	if code != 400 || !strings.Contains(string(body), `"id":"svc"`) || !strings.Contains(string(body), "name is required") {
		t.Errorf("service error: %d %s", code, body)
	}
	// any other gRPC error maps by code, with the service as id
	code, body = httpError(status.Error(codes.ResourceExhausted, "slow down"), "svc")
	e := &merr.Error{}
	_ = json.Unmarshal(body, e)
	if code != 429 || e.Id != "svc" || e.Detail != "slow down" || e.Code != 429 {
		t.Errorf("plain gRPC error: %d %s", code, body)
	}
	if code, _ = httpError(status.Error(codes.DataLoss, "x"), "svc"); code != 500 {
		t.Errorf("unmapped code: %d", code)
	}
}

func TestHTTPMetadata(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer t")
	h.Set("X-Tenant", "t1")
	h.Set("Connection", "keep-alive")
	h.Set("Content-Type", "application/json")
	h.Set("Proxy-Authorization", "secret")
	h["Bad Key!"] = []string{"v"}
	md := httpMetadata(h)
	if md.Get("authorization")[0] != "Bearer t" || md.Get("x-tenant")[0] != "t1" {
		t.Errorf("headers lost: %v", md)
	}
	for _, k := range []string{"connection", "content-type", "proxy-authorization", "bad key!"} {
		if len(md.Get(k)) != 0 {
			t.Errorf("%s forwarded: %v", k, md)
		}
	}
}
