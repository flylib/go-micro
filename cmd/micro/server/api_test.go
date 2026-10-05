package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flylib/go-micro/client"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/server"
)

type HelloRequest struct {
	Name string `json:"name"`
}

type HelloResponse struct {
	Msg    string `json:"msg"`
	Header string `json:"header,omitempty"`
}

type Greeter struct{}

func (g *Greeter) Hello(ctx context.Context, req *HelloRequest, rsp *HelloResponse) error {
	if req.Name == "" {
		return merr.BadRequest("greeter", "name is required")
	}
	rsp.Msg = "Hello " + req.Name
	rsp.Header, _ = metadata.Get(ctx, "X-Test")
	return nil
}

// testAPI starts a greeter service on a memory registry and returns an
// HTTP server that serves /api like the dashboard, and the registry.
func testAPI(t *testing.T) (*httptest.Server, registry.Registry) {
	t.Helper()
	reg := registry.NewMemoryRegistry()
	srv := server.NewServer(server.Name("greeter"), server.Registry(reg), server.Address("127.0.0.1:0"))
	if err := srv.Handle(srv.NewHandler(new(Greeter))); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })

	a := &apiCaller{client: client.NewClient(client.Registry(reg)), registry: reg}
	allow := func(http.ResponseWriter, *http.Request, string) bool { return true }
	mux := http.NewServeMux()
	mux.HandleFunc(apiPrefix, func(w http.ResponseWriter, r *http.Request) { a.serveAPI(w, r, allow) })
	registerHealth(mux, reg)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs, reg
}

func post(t *testing.T, url, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPICall(t *testing.T) {
	hs, _ := testAPI(t)
	for _, path := range []string{"/api/greeter/Greeter/Hello", "/api/greeter/Greeter.Hello"} {
		code, out := post(t, hs.URL+path, `{"name":"Alice"}`, "X-Test", "v1")
		if code != 200 || out["msg"] != "Hello Alice" || out["header"] != "v1" {
			t.Fatalf("%s: %d %v", path, code, out)
		}
	}

	code, out := post(t, hs.URL+"/api/greeter/Greeter/Hello", `{}`)
	if code != 400 || out["id"] != "greeter" || out["detail"] != "name is required" {
		t.Fatalf("service error: %d %v", code, out)
	}
	code, out = post(t, hs.URL+"/api/nobody/Svc/Call", `{}`)
	if code != 404 || out["id"] != apiErrorID {
		t.Fatalf("unknown service: %d %v", code, out)
	}
	code, _ = post(t, hs.URL+"/api/greeter/Greeter/Hello", `not json`)
	if code != 400 {
		t.Fatalf("non-JSON body: %d", code)
	}
	code, _ = post(t, hs.URL+"/api/greeter", `{}`)
	if code != 404 {
		t.Fatalf("path without an endpoint: %d", code)
	}
	resp, err := http.Get(hs.URL + "/api/greeter/Greeter/Hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
}

func TestAPISkipsGRPCNodes(t *testing.T) {
	hs, reg := testAPI(t)
	// a second greeter on the gRPC server, unreachable for the default client
	if err := reg.Register(&registry.Service{Name: "greeter", Nodes: []*registry.Node{
		{Id: "greeter-grpc", Address: "127.0.0.1:1", Metadata: map[string]string{"protocol": "grpc"}},
	}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if code, out := post(t, hs.URL+"/api/greeter/Greeter/Hello", `{"name":"A"}`); code != 200 {
			t.Fatalf("call %d: %d %v", i, code, out)
		}
	}
}

func TestAPIRefusesGRPCServices(t *testing.T) {
	hs, reg := testAPI(t)
	if err := reg.Register(&registry.Service{Name: "orders", Nodes: []*registry.Node{
		{Id: "orders-1", Address: "127.0.0.1:1", Metadata: map[string]string{"protocol": "grpc"}},
	}}); err != nil {
		t.Fatal(err)
	}
	code, out := post(t, hs.URL+"/api/orders/Orders/Get", `{}`)
	if code != 501 || !strings.Contains(out["detail"].(string), "gRPC") {
		t.Fatalf("gRPC service: %d %v", code, out)
	}
}

func TestHealth(t *testing.T) {
	hs, _ := testAPI(t)
	for _, p := range []string{"/health", "/health/live", "/health/ready"} {
		resp, err := http.Get(hs.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != 200 || out["status"] != "ok" {
			t.Fatalf("%s: %d %v", p, resp.StatusCode, out)
		}
	}
}

type failingRegistry struct{ registry.Registry }

func (failingRegistry) ListServices(...registry.ListOption) ([]*registry.Service, error) {
	return nil, errors.New("registry down")
}

func TestHealthNotReady(t *testing.T) {
	mux := http.NewServeMux()
	registerHealth(mux, failingRegistry{registry.NewMemoryRegistry()})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if rec.Code != 503 {
		t.Fatalf("ready with the registry down: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if rec.Code != 200 {
		t.Fatalf("live with the registry down: %d", rec.Code)
	}
}

func TestSplitAPIPath(t *testing.T) {
	for path, want := range map[string]string{
		"/api/s/H/M":    "s H.M",
		"/api/s/H.M":    "s H.M",
		"/api/a.b/H/M/": "a.b H.M",
		"/api/s":        "",
		"/api/s/H":      "",
		"/api/s/.M":     "",
		"/api/s/H/M/x":  "",
		"/api/s/H.M.x":  "",
		"/api//H/M":     "",
		"/api/s/H.":     "",
	} {
		svc, ep, ok := splitAPIPath(path)
		got := ""
		if ok {
			got = svc + " " + ep
		}
		if got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
