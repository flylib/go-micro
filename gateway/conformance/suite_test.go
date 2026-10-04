package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flylib/go-micro/registry"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/grpc/codes"
	pb "google.golang.org/grpc/interop/grpc_testing"
)

// suite is the shared fixture of the gateway cases: one registry, one rules
// source, one gateway connection and a long-lived echo service that the
// rules-applied probe routes point at.
type suite struct {
	cfg     config
	reg     registry.Registry
	rw      rulesWriter
	gw      *client
	schema  *jsonschema.Schema
	run     string   // per-run prefix keeps service names unique across runs
	echo    string   // base service behind every probe route
	echoSrv *backend // deregistered by TestMain
	probes  int
}

// TestMain deregisters the shared echo service, which outlives every test,
// so a run leaves nothing behind in the registry.
func TestMain(m *testing.M) {
	code := m.Run()
	if shared != nil {
		_ = shared.echoSrv.stop()
		_ = shared.rw.close()
		_ = shared.gw.close()
	}
	os.Exit(code)
}

var (
	setupOnce sync.Once
	shared    *suite
	setupErr  error
)

// gatewaySuite returns the shared fixture, skipping the test when no
// gateway is configured.
func gatewaySuite(t *testing.T) *suite {
	t.Helper()
	cfg := loadConfig()
	if cfg.gateway == "" {
		t.Skip("GATEWAY_ADDR not set: gateway conformance cases skipped")
	}
	setupOnce.Do(func() { shared, setupErr = newSuite(cfg) })
	if setupErr != nil {
		t.Fatalf("suite setup: %v", setupErr)
	}
	return shared
}

func newSuite(cfg config) (*suite, error) {
	if cfg.rules == "" || cfg.rules == "none" {
		return nil, fmt.Errorf("MICRO_GATEWAY_RULES must name the rules source the gateway loads")
	}
	schema, err := loadSchema()
	if err != nil {
		return nil, err
	}
	reg, err := cfg.newRegistry()
	if err != nil {
		return nil, err
	}
	rw, err := newRulesWriter(cfg.rules)
	if err != nil {
		return nil, err
	}
	gw, err := dial(cfg.gateway)
	if err != nil {
		return nil, err
	}
	s := &suite{cfg: cfg, reg: reg, rw: rw, gw: gw, schema: schema, run: "c" + randHex(3)}
	s.echo = s.svc("echo")
	// the base echo outlives every test: it backs the probe routes
	if s.echoSrv, err = startBackend(reg, cfg.advertiseHost, s.echo); err != nil {
		return nil, fmt.Errorf("start echo backend: %w", err)
	}
	return s, nil
}

// schemaJSON is a copy of gateway/rules.schema.json, embedded so the
// compiled suite runs from any directory; TestHarness_SchemaInSync fails
// when the two drift.
//
//go:embed schema.json
var schemaJSON []byte

func loadSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("rules.schema.json", doc); err != nil {
		return nil, err
	}
	return c.Compile("rules.schema.json")
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// svc returns a service name unique to this run.
func (s *suite) svc(name string) string { return s.run + "-" + name }

// start starts a backend node of service name, stopped at test end. Each
// node gets its own registry client, as each service process has one:
// a Nacos 2.x client holds a single instance per service, so nodes
// sharing a client would replace each other.
func (s *suite) start(t *testing.T, name string, opts ...backendOption) *backend {
	t.Helper()
	reg, err := s.cfg.newRegistry()
	if err != nil {
		t.Fatal(err)
	}
	b, err := startBackend(reg, s.cfg.advertiseHost, name, opts...)
	if err != nil {
		t.Fatalf("start backend %s: %v", name, err)
	}
	t.Cleanup(func() { _ = b.stop() })
	return b
}

// apply publishes r plus a fresh probe route and blocks until the gateway
// serves the probe, i.e. until r is live. It returns how long that took
// and fails the test past the spec's 5s (SPEC 10).
func (s *suite) apply(t *testing.T, r rules) time.Duration {
	t.Helper()
	s.probes++
	probe := path(s.svc(fmt.Sprintf("probe%d", s.probes)), "TestService", "UnaryCall")
	routes, _ := r["routes"].([]route)
	doc := rules{}
	for k, v := range r {
		doc[k] = v
	}
	doc["version"] = 1
	doc["routes"] = append(append([]route{}, routes...), route{
		"name":     "conformance-probe",
		"match":    map[string]any{"method": probe},
		"upstream": map[string]any{"service": s.echo},
	})
	if err := s.validate(doc); err != nil {
		t.Fatalf("suite bug, rules violate rules.schema.json: %v", err)
	}
	start := time.Now()
	s.write(t, doc)
	deadline := start.Add(propagation + grace)
	for {
		e, err := s.gw.call(context.Background(), probe, nil)
		if err == nil && e.Service == s.echo {
			return time.Since(start)
		}
		if time.Now().After(deadline) {
			t.Fatalf("rules not live after %s (SPEC 10 allows %s); last probe: %v", propagation+grace, propagation, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// reset applies empty rules: convention routing, no plugins.
func (s *suite) reset(t *testing.T) {
	t.Helper()
	s.apply(t, rules{})
}

func (s *suite) validate(doc rules) error {
	b, err := encodeRules(doc)
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	return s.schema.Validate(v)
}

// write publishes doc without waiting for it to be applied.
func (s *suite) write(t *testing.T, doc rules) {
	t.Helper()
	b, err := encodeRules(doc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.rw.write(ctx, b); err != nil {
		t.Fatalf("publish rules to %s: %v", s.cfg.rules, err)
	}
}

// call expects a successful unary call and returns the backend echo.
func (s *suite) call(t *testing.T, method string, md ...string) echo {
	t.Helper()
	e, err := s.gw.call(context.Background(), method, nil, md...)
	if err != nil {
		t.Fatalf("call %s: %s", method, failureOf(err))
	}
	return e
}

// awaitNodes calls method until every given node has answered — or, with
// no nodes given, until any call succeeds — within the registry
// propagation window (SPEC 4.4).
func (s *suite) awaitNodes(t *testing.T, method string, nodes ...*backend) {
	t.Helper()
	want := map[string]bool{}
	for _, b := range nodes {
		want[b.id] = true
	}
	deadline := time.Now().Add(propagation + grace)
	for answered := false; !answered || len(want) > 0; {
		if e, err := s.gw.call(context.Background(), method, nil); err == nil {
			answered = true
			delete(want, e.Node)
		}
		if time.Now().After(deadline) {
			t.Fatalf("nodes never received traffic within %s: %v", propagation+grace, keys(want))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// gatewayError asserts err is a gateway-originated error (SPEC 8): the
// given gRPC status and a go-micro error with id micro.gateway.
func gatewayError(t *testing.T, err error, code codes.Code, httpCode int32) {
	t.Helper()
	if err == nil {
		t.Fatalf("call succeeded, want %s", code)
	}
	f := failureOf(err)
	if f.code != code {
		t.Errorf("status = %s, want %s (%s)", f.code, code, f)
	}
	if f.micro == nil {
		t.Errorf("grpc-message is not a go-micro error: %s", f)
		return
	}
	if f.micro.Id != "micro.gateway" || f.micro.Code != httpCode {
		t.Errorf("error = {id:%q code:%d}, want {id:micro.gateway code:%d}", f.micro.Id, f.micro.Code, httpCode)
	}
}

// load calls method in a loop until stopped and counts the outcomes.
type load struct {
	ok, failed atomic.Int64
	mu         sync.Mutex
	errs       []string
	cancel     context.CancelFunc
	done       chan struct{}
}

func (s *suite) startLoad(method string) *load {
	ctx, cancel := context.WithCancel(context.Background())
	l := &load{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for ctx.Err() == nil {
			if _, err := s.gw.call(context.Background(), method, &pb.SimpleRequest{}); err != nil {
				l.failed.Add(1)
				l.mu.Lock()
				if len(l.errs) < 5 {
					l.errs = append(l.errs, failureOf(err).String())
				}
				l.mu.Unlock()
			} else {
				l.ok.Add(1)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	return l
}

func (l *load) stop() {
	l.cancel()
	<-l.done
}

func (l *load) assertClean(t *testing.T) {
	t.Helper()
	if l.ok.Load() == 0 {
		t.Fatal("load generator made no successful calls")
	}
	if n := l.failed.Load(); n > 0 {
		t.Errorf("%d of %d calls failed, first errors: %v", n, n+l.ok.Load(), l.errs)
	}
}
