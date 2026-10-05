package inspect

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/flylib/go-micro/registry"
)

// grpcNode starts a gRPC server; with h it serves the health service.
func grpcNode(t *testing.T, h *health.Server) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	if h != nil {
		healthpb.RegisterHealthServer(s, h)
	}
	go func() { _ = s.Serve(l) }()
	t.Cleanup(s.Stop)
	return l.Addr().String()
}

// closedAddr is an address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func register(t *testing.T, reg registry.Registry, service, id, addr, protocol string) {
	t.Helper()
	if err := reg.Register(&registry.Service{Name: service, Version: "v1", Nodes: []*registry.Node{
		{Id: id, Address: addr, Metadata: map[string]string{"protocol": protocol}},
	}}); err != nil {
		t.Fatal(err)
	}
}

type recorder struct{ events []Event }

func (r *recorder) Notify(_ context.Context, e Event) error {
	r.events = append(r.events, e)
	return nil
}

func changes(events []Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Node.ID+":"+string(e.From)+">"+string(e.To))
	}
	sort.Strings(out)
	return out
}

func TestRound(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	sick := health.NewServer()
	sick.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)

	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()

	register(t, reg, "users", "users-ok", grpcNode(t, health.NewServer()), "grpc")
	register(t, reg, "users", "users-sick", grpcNode(t, sick), "grpc")
	register(t, reg, "users", "users-zombie", closedAddr(t), "grpc")
	register(t, reg, "legacy", "legacy-nohealth", grpcNode(t, nil), "grpc")
	register(t, reg, "mucp", "mucp-tcp", tcp.Addr().String(), "mucp")

	rec := &recorder{}
	in := New(reg, Options{Threshold: 2, Timeout: 500 * time.Millisecond, Notifier: rec})
	ctx := context.Background()

	// first bad round: below the threshold, nothing reported yet
	if ev, err := in.Round(ctx); err != nil || len(ev) != 0 {
		t.Fatalf("round 1: %v %v", ev, err)
	}
	ev, err := in.Round(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := changes(ev); strings.Join(got, ",") != "users-sick:ok>unhealthy,users-zombie:ok>unreachable" {
		t.Fatalf("round 2 events %v", got)
	}
	states := map[string]State{}
	for _, n := range in.Nodes() {
		states[n.ID] = n.State
	}
	want := map[string]State{"users-ok": StateOK, "users-sick": StateUnhealthy, "users-zombie": StateUnreachable,
		"legacy-nohealth": StateOK, "mucp-tcp": StateOK}
	for id, st := range want {
		if states[id] != st {
			t.Errorf("%s: %s, want %s", id, states[id], st)
		}
	}
	if len(rec.events) != 2 {
		t.Fatalf("notifier got %d events", len(rec.events))
	}

	// recovery after one good round; a reported node that leaves the registry
	sick.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	for _, n := range in.Nodes() {
		if n.ID == "users-zombie" {
			_ = reg.Deregister(&registry.Service{Name: "users", Version: "v1", Nodes: []*registry.Node{{Id: n.ID, Address: n.Address}}})
		}
	}
	ev, _ = in.Round(ctx)
	if got := changes(ev); strings.Join(got, ",") != "users-sick:unhealthy>ok,users-zombie:unreachable>deregistered" {
		t.Fatalf("round 3 events %v", got)
	}
	if ev, _ := in.Round(ctx); len(ev) != 0 {
		t.Fatalf("steady state reported %v", changes(ev))
	}
}

func TestServicesFilter(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	register(t, reg, "a", "a-1", closedAddr(t), "grpc")
	register(t, reg, "b", "b-1", closedAddr(t), "grpc")
	in := New(reg, Options{Threshold: 1, Timeout: 200 * time.Millisecond, Services: []string{"b"}})
	ev, err := in.Round(context.Background())
	if err != nil || len(ev) != 1 || ev[0].Node.Service != "b" {
		t.Fatalf("filtered round: %v %v", changes(ev), err)
	}
}

func event() Event {
	return Event{
		Node: Node{Service: "users", Version: "v1", ID: "users-1", Address: "10.0.0.1:9001", Failures: 3, Error: "connection refused"},
		From: StateOK, To: StateUnreachable, Time: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	}
}

func TestWebhookPayloads(t *testing.T) {
	var gotURL string
	var got map[string]any
	answer := `{"code":0}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		b, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	fixed := func() time.Time { return time.Unix(1700000000, 0) }
	ctx := context.Background()

	w := &Webhook{URL: srv.URL + "/hook", Kind: KindFeishu, Secret: "s3cret", now: fixed}
	if err := w.Notify(ctx, event()); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("1700000000\ns3cret"))
	if got["msg_type"] != "text" || got["timestamp"] != "1700000000" || got["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("feishu payload %v", got)
	}
	if text := got["content"].(map[string]any)["text"].(string); !strings.Contains(text, "users") || !strings.Contains(text, "unreachable") {
		t.Fatalf("feishu text %q", text)
	}

	answer = `{"errcode":0}`
	w = &Webhook{URL: srv.URL + "/robot/send?access_token=x", Kind: KindDingTalk, Secret: "s3cret", now: fixed}
	if err := w.Notify(ctx, event()); err != nil {
		t.Fatal(err)
	}
	dmac := hmac.New(sha256.New, []byte("s3cret"))
	dmac.Write([]byte("1700000000000\ns3cret"))
	if !strings.Contains(gotURL, "access_token=x") || !strings.Contains(gotURL, "timestamp=1700000000000") ||
		!strings.Contains(gotURL, "sign="+urlQueryEscape(base64.StdEncoding.EncodeToString(dmac.Sum(nil)))) {
		t.Fatalf("dingtalk url %s", gotURL)
	}
	if got["msgtype"] != "text" {
		t.Fatalf("dingtalk payload %v", got)
	}

	w = &Webhook{URL: srv.URL, Kind: KindWeCom}
	if err := w.Notify(ctx, event()); err != nil || got["msgtype"] != "text" {
		t.Fatalf("wecom: %v %v", got, err)
	}

	w = &Webhook{URL: srv.URL}
	if err := w.Notify(ctx, event()); err != nil || got["to"] != "unreachable" {
		t.Fatalf("json: %v %v", got, err)
	}

	// a bot rejecting the message is an error
	answer = `{"errcode":310000,"errmsg":"sign not match"}`
	if err := (&Webhook{URL: srv.URL, Kind: KindDingTalk}).Notify(ctx, event()); err == nil || !strings.Contains(err.Error(), "sign not match") {
		t.Fatalf("bot error not reported: %v", err)
	}
	if err := (&Webhook{URL: srv.URL, Kind: "slack"}).Notify(ctx, event()); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D")
	return r.Replace(s)
}
