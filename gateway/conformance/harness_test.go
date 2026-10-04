package conformance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flylib/go-micro/registry"
	"google.golang.org/grpc/codes"
	pb "google.golang.org/grpc/interop/grpc_testing"
)

// These tests check the harness itself, calling backends directly with no
// gateway in between, so a red conformance run points at the gateway and
// not at the suite. They need no environment and always run.

func directBackend(t *testing.T, reg registry.Registry, name string, opts ...backendOption) (*backend, *client) {
	t.Helper()
	b, err := startBackend(reg, "127.0.0.1", name, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.stop() })
	c, err := dial(b.address())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.close() })
	return b, c
}

func TestHarness_BackendAnswersAnyPackage(t *testing.T) {
	b, c := directBackend(t, registry.NewMemoryRegistry(), "svc")
	ctx := context.Background()
	for _, m := range []string{
		path("svc", "TestService", "UnaryCall"),
		path("some.other.pkg", "TestService", "UnaryCall"),
		path("svc", "TestService", "Echo"),
		path("svc", "Alt", "UnaryCall"),
	} {
		e, err := c.call(ctx, m, nil, "x-probe", "1")
		if err != nil {
			t.Fatalf("%s: %s", m, failureOf(err))
		}
		if e.Service != "svc" || e.Node != b.id || e.Version != "v1" {
			t.Errorf("%s: echo %+v", m, e)
		}
		if v, _ := e.header("x-probe"); v != "1" {
			t.Errorf("%s: metadata not echoed: %v", m, e.Metadata)
		}
	}
}

func TestHarness_ServiceErrorShape(t *testing.T) {
	_, c := directBackend(t, registry.NewMemoryRegistry(), "svc")
	req := &pb.SimpleRequest{ResponseStatus: &pb.EchoStatus{Code: 400, Message: "bad"}}
	_, err := c.call(context.Background(), path("svc", "TestService", "UnaryCall"), req)
	f := failureOf(err)
	if f.code != codes.InvalidArgument || f.micro == nil || f.micro.Id != "svc" || f.micro.Code != 400 {
		t.Fatalf("got %s, want InvalidArgument with go-micro error {id:svc code:400}", f)
	}
}

func TestHarness_Streaming(t *testing.T) {
	_, c := directBackend(t, registry.NewMemoryRegistry(), "svc")
	ctx := context.Background()
	if got, err := c.serverStream(ctx, path("svc", "TestService", "StreamingOutputCall"), 3); err != nil || len(got) != 3 {
		t.Errorf("server stream: %d replies, err %v", len(got), err)
	}
	if got, err := c.bidiStream(ctx, path("svc", "TestService", "FullDuplexCall"), 3); err != nil || len(got) != 3 {
		t.Errorf("bidi stream: %d replies, err %v", len(got), err)
	}
}

func TestHarness_RegistersProtocolAndVersion(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	g, err := startBackend(reg, "127.0.0.1", "svc", withVersion("v2"))
	if err != nil {
		t.Fatal(err)
	}
	defer g.stop()
	m, err := startBackend(reg, "127.0.0.1", "svc", withMUCP())
	if err != nil {
		t.Fatal(err)
	}
	defer m.stop()

	services, err := reg.GetService("svc")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range services {
		for _, n := range s.Nodes {
			got[n.Id] = s.Version + "/" + n.Metadata["protocol"]
		}
	}
	if got[g.id] != "v2/grpc" || got[m.id] != "v1/mucp" {
		t.Fatalf("registered %v, want %s=v2/grpc and %s=v1/mucp", got, g.id, m.id)
	}

	if err := g.stop(); err != nil {
		t.Fatal(err)
	}
	services, _ = reg.GetService("svc")
	for _, s := range services {
		for _, n := range s.Nodes {
			if n.Id == g.id {
				t.Fatal("stopped backend still registered")
			}
		}
	}
}

func TestHarness_FileRulesWriter(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rules.json")
	w, err := newRulesWriter("file://" + p)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := encodeRules(rules{"version": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.write(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(doc) {
		t.Fatalf("file holds %q, want %q", got, doc)
	}
}

func TestHarness_SchemaInSync(t *testing.T) {
	canonical, err := os.ReadFile("../rules.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(canonical) != string(schemaJSON) {
		t.Fatal("schema.json differs from gateway/rules.schema.json: cp ../rules.schema.json schema.json")
	}
}

func TestHarness_SchemaAcceptsSuiteRules(t *testing.T) {
	schema, err := loadSchema()
	if err != nil {
		t.Fatal(err)
	}
	s := &suite{schema: schema}
	_, pub, _ := rsaKeys(t)
	docs := []rules{
		{"version": 1},
		{"version": 1, "routes": []route{
			jwtRoute("a", "svc", pub, "admin"),
			{"name": "b", "match": map[string]any{"prefix": "/x."}, "upstream": map[string]any{"service": "y", "selector": map[string]any{"version_weights": map[string]int{"v1": 100, "v2": 0}}}},
			{"name": "c", "match": map[string]any{"service": "z"}, "plugins": []any{map[string]any{"name": "rate-limit", "config": map[string]any{"rate": 1, "burst": 0, "key": "client_ip"}}}},
		}},
	}
	for i, d := range docs {
		if err := s.validate(d); err != nil {
			t.Errorf("doc %d rejected: %v", i, err)
		}
	}
}
