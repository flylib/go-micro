package conformance

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/flylib/go-micro/auth"
	"github.com/flylib/go-micro/auth/jwt/token"
	"google.golang.org/grpc/codes"
	pb "google.golang.org/grpc/interop/grpc_testing"
)

// Each test is one case of gateway/SPEC.md section 14, named by its ID.
// Tests run in file order and each starts by applying the rules it needs.

func TestR1_ConventionRoute(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("r1")
	s.start(t, svc)

	m := path(svc, "TestService", "UnaryCall")
	s.awaitNodes(t, m) // registration may lag; no node ids needed
	if e := s.call(t, m); e.Service != svc {
		t.Fatalf("answered by %q, want %q", e.Service, svc)
	}
}

func TestR2_RoutePrecedence(t *testing.T) {
	s := gatewaySuite(t)
	a, b, c := s.svc("r2a"), s.svc("r2b"), s.svc("r2c")
	for _, name := range []string{a, b, c} {
		s.start(t, name)
	}
	y, z := s.svc("r2y"), s.svc("r2z") // path packages with no nodes of their own

	// listed in reverse precedence order: list order must not matter
	s.apply(t, rules{"routes": []route{
		{"name": "r2-service", "match": map[string]any{"service": y}, "upstream": map[string]any{"service": c}},
		{"name": "r2-prefix", "match": map[string]any{"prefix": "/" + y + ".Alt/"}, "upstream": map[string]any{"service": b}},
		{"name": "r2-method", "match": map[string]any{"method": path(y, "TestService", "Echo")}, "upstream": map[string]any{"service": a}},
		{"name": "r2-short-prefix", "match": map[string]any{"prefix": "/" + z + "."}, "upstream": map[string]any{"service": c}},
		{"name": "r2-long-prefix", "match": map[string]any{"prefix": "/" + z + ".Alt/"}, "upstream": map[string]any{"service": b}},
	}})

	cases := []struct{ path, want, why string }{
		{path(y, "TestService", "Echo"), a, "method beats service"},
		{path(y, "Alt", "UnaryCall"), b, "prefix beats service"},
		{path(y, "TestService", "UnaryCall"), c, "service route"},
		{path(z, "Alt", "UnaryCall"), b, "longest prefix wins"},
		{path(z, "TestService", "UnaryCall"), c, "shorter prefix"},
	}
	for _, tc := range cases {
		if e := s.call(t, tc.path); e.Service != tc.want {
			t.Errorf("%s (%s): answered by %q, want %q", tc.path, tc.why, e.Service, tc.want)
		}
	}
}

func TestR3_NoServiceInPath(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	_, err := s.gw.call(context.Background(), "/TestService/UnaryCall", nil)
	gatewayError(t, err, codes.Unimplemented, 501)
}

func TestR4_MalformedPath(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	// rejected before routing, possibly by the gRPC library itself, so
	// only the status is specified (SPEC 3.1)
	_, err := s.gw.call(context.Background(), "/malformed", nil)
	if got := failureOf(err).code; got != codes.Unimplemented {
		t.Fatalf("status = %s, want Unimplemented", got)
	}
}

func TestD1_ScaleUp(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("d1")
	m := path(svc, "TestService", "UnaryCall")
	n1, n2 := s.start(t, svc), s.start(t, svc)
	s.awaitNodes(t, m, n1, n2)

	n3 := s.start(t, svc)
	s.awaitNodes(t, m, n3)
}

func TestD2_ScaleDownUnderLoad(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("d2")
	m := path(svc, "TestService", "UnaryCall")
	keep, n2, n3 := s.start(t, svc), s.start(t, svc), s.start(t, svc)
	s.awaitNodes(t, m, keep, n2, n3)

	l := s.startLoad(m)
	time.Sleep(300 * time.Millisecond)
	if err := n2.stop(); err != nil {
		t.Fatal(err)
	}
	if err := n3.stop(); err != nil {
		t.Fatal(err)
	}
	// long enough for the gateway to drop the stopped nodes (SPEC 4.4)
	time.Sleep(propagation + grace)
	l.stop()
	l.assertClean(t)
}

func TestD3_SkipsMUCPNodes(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("d3")
	m := path(svc, "TestService", "UnaryCall")
	g1, g2 := s.start(t, svc), s.start(t, svc)
	mucp := s.start(t, svc, withMUCP())
	s.awaitNodes(t, m, g1, g2)

	for i := 0; i < 30; i++ {
		if e := s.call(t, m); e.Node == mucp.id {
			t.Fatalf("call %d reached mucp node %s", i, mucp.id)
		}
	}
	if n := mucp.calls.Load(); n > 0 {
		t.Fatalf("mucp node handled %d calls", n)
	}
}

func TestD4_RegistryOutage(t *testing.T) {
	s := gatewaySuite(t)
	if s.cfg.registryStop == "" || s.cfg.registryStart == "" {
		t.Skip("CONFORMANCE_REGISTRY_STOP/START not set")
	}
	s.reset(t)
	svc := s.svc("d4")
	m := path(svc, "TestService", "UnaryCall")
	n1, n2 := s.start(t, svc), s.start(t, svc)
	s.awaitNodes(t, m, n1, n2)

	shell(t, s.cfg.registryStop)
	t.Cleanup(func() { shell(t, s.cfg.registryStart) })

	// across more than one refresh window, with the registry gone
	l := s.startLoad(m)
	time.Sleep(2 * (propagation + grace))
	l.stop()
	l.assertClean(t)
}

func shell(t *testing.T, cmd string) {
	t.Helper()
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cmd, err, out)
	}
}

func TestL1_VersionWeights(t *testing.T) {
	s := gatewaySuite(t)
	svc := s.svc("l1")
	m := path(svc, "TestService", "UnaryCall")
	v1 := s.start(t, svc, withVersion("v1"))
	v2 := s.start(t, svc, withVersion("v2"))
	s.reset(t)
	s.awaitNodes(t, m, v1, v2) // both versions are live before the split

	s.apply(t, rules{"routes": []route{{
		"name":     "l1",
		"match":    map[string]any{"service": svc},
		"upstream": map[string]any{"selector": map[string]any{"version_weights": map[string]int{"v1": 100, "v2": 0}}},
	}}})
	before := v2.calls.Load()
	for i := 0; i < 40; i++ {
		if e := s.call(t, m); e.Version != "v1" {
			t.Fatalf("call %d answered by version %q", i, e.Version)
		}
	}
	if n := v2.calls.Load() - before; n > 0 {
		t.Fatalf("v2 handled %d calls with weight 0", n)
	}
}

func TestE1_ServiceErrorPassesThrough(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("e1")
	s.start(t, svc)
	m := path(svc, "TestService", "UnaryCall")
	s.awaitNodes(t, m)

	req := &pb.SimpleRequest{ResponseStatus: &pb.EchoStatus{Code: 400, Message: "name is required"}}
	_, err := s.gw.call(context.Background(), m, req)
	if err == nil {
		t.Fatal("call succeeded, want InvalidArgument")
	}
	f := failureOf(err)
	if f.code != codes.InvalidArgument {
		t.Errorf("status = %s, want InvalidArgument", f.code)
	}
	if f.micro == nil || f.micro.Id != svc || f.micro.Code != 400 || f.micro.Detail != "name is required" {
		t.Errorf("service error altered in transit: %s", f)
	}
}

func TestE2_NoEligibleNodes(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	_, err := s.gw.call(context.Background(), path(s.svc("e2-nonode"), "TestService", "UnaryCall"), nil)
	gatewayError(t, err, codes.Unavailable, 503)
}

var traceparentRe = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

func TestH1_Traceparent(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("h1")
	s.start(t, svc)
	m := path(svc, "TestService", "UnaryCall")
	s.awaitNodes(t, m)

	tp, _ := s.call(t, m).header("traceparent")
	g := traceparentRe.FindStringSubmatch(tp)
	if g == nil || strings.Trim(g[1], "0") == "" || strings.Trim(g[2], "0") == "" {
		t.Errorf("without client traceparent the service got %q, want a generated valid one", tp)
	}

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	sent := "00-" + traceID + "-00f067aa0ba902b7-01"
	tp, _ = s.call(t, m, "traceparent", sent).header("traceparent")
	if g := traceparentRe.FindStringSubmatch(tp); g == nil || g[1] != traceID {
		t.Errorf("client trace %s not continued: service got %q", traceID, tp)
	}
}

func TestH2_ReservedHeaders(t *testing.T) {
	s := gatewaySuite(t)
	svc := s.svc("h2")
	s.start(t, svc)
	s.apply(t, rules{"routes": []route{{"name": "h2-route", "match": map[string]any{"service": svc}}}})
	m := path(svc, "TestService", "UnaryCall")
	s.awaitNodes(t, m)

	e := s.call(t, m, "micro-gateway-account", "spoofed", "micro-gateway-route", "spoofed")
	if v, ok := e.header("micro-gateway-account"); ok {
		t.Errorf("client-supplied micro-gateway-account reached the service: %q", v)
	}
	if v, _ := e.header("micro-gateway-route"); v != "h2-route" {
		t.Errorf("micro-gateway-route = %q, want h2-route", v)
	}
	if v, _ := e.header("x-forwarded-for"); v == "" {
		t.Error("x-forwarded-for not set")
	}
}

func TestP1_IPRestriction(t *testing.T) {
	s := gatewaySuite(t)
	denied, open := s.svc("p1-denied"), s.svc("p1-open")
	s.start(t, denied)
	s.start(t, open)
	s.apply(t, rules{"routes": []route{{
		"name":    "p1",
		"match":   map[string]any{"service": denied},
		"plugins": []any{map[string]any{"name": "ip-restriction", "config": map[string]any{"deny": []string{"0.0.0.0/0", "::/0"}}}},
	}}})

	_, err := s.gw.call(context.Background(), path(denied, "TestService", "UnaryCall"), nil)
	gatewayError(t, err, codes.PermissionDenied, 403)
	s.awaitNodes(t, path(open, "TestService", "UnaryCall")) // route plugins stay on their route
}

// rsaKeys returns a key pair in auth/jwt's encoding: base64 of PEM.
func rsaKeys(t *testing.T) (priv, pub string, pubPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return base64.StdEncoding.EncodeToString(privPEM), base64.StdEncoding.EncodeToString(pubPEM), pubPEM
}

func jwtRoute(name, svc, pub string, scopes ...string) route {
	cfg := map[string]any{"public_key": pub}
	if len(scopes) > 0 {
		cfg["scopes"] = scopes
	}
	return route{
		"name":    name,
		"match":   map[string]any{"service": svc},
		"plugins": []any{map[string]any{"name": "jwt-auth", "config": cfg}},
	}
}

func TestP2_JWTAuth(t *testing.T) {
	s := gatewaySuite(t)
	open, admin := s.svc("p2"), s.svc("p2-admin")
	s.start(t, open)
	s.start(t, admin)
	priv, pub, _ := rsaKeys(t)
	s.apply(t, rules{"routes": []route{
		jwtRoute("p2", open, pub),
		jwtRoute("p2-admin", admin, pub, "admin"),
	}})

	tok, err := token.New(token.WithPrivateKey(priv), token.WithPublicKey(pub)).
		Generate(&auth.Account{ID: "user-42", Scopes: []string{"orders"}}, token.WithExpiry(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	m := path(open, "TestService", "UnaryCall")

	_, err = s.gw.call(context.Background(), m, nil)
	gatewayError(t, err, codes.Unauthenticated, 401)
	_, err = s.gw.call(context.Background(), m, nil, "authorization", "Bearer not-a-jwt")
	gatewayError(t, err, codes.Unauthenticated, 401)

	e, err := s.gw.call(context.Background(), m, nil, "authorization", "Bearer "+tok.Token)
	if err != nil {
		t.Fatalf("valid auth/jwt token rejected: %s", failureOf(err))
	}
	if v, _ := e.header("micro-gateway-account"); v != "user-42" {
		t.Errorf("micro-gateway-account = %q, want user-42", v)
	}
	if v, _ := e.header("authorization"); v != "Bearer "+tok.Token {
		t.Error("authorization not forwarded unchanged")
	}

	_, err = s.gw.call(context.Background(), path(admin, "TestService", "UnaryCall"), nil, "authorization", "Bearer "+tok.Token)
	gatewayError(t, err, codes.PermissionDenied, 403)
}

// hs256 signs claims with HMAC-SHA256, as an attacker would when reusing
// the RSA public key as the HMAC secret.
func hs256(t *testing.T, secret []byte, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "HS256", "typ": "JWT"}) + "." + enc(claims)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	return signing + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestP3_JWTAlgorithmConfusion(t *testing.T) {
	s := gatewaySuite(t)
	svc := s.svc("p3")
	s.start(t, svc)
	_, pub, pubPEM := rsaKeys(t)
	s.apply(t, rules{"routes": []route{jwtRoute("p3", svc, pub)}})

	claims := map[string]any{"sub": "attacker", "exp": time.Now().Add(time.Hour).Unix()}
	for name, secret := range map[string][]byte{"PEM": pubPEM, "base64 PEM": []byte(pub)} {
		_, err := s.gw.call(context.Background(), path(svc, "TestService", "UnaryCall"), nil,
			"authorization", "Bearer "+hs256(t, secret, claims))
		t.Run(name, func(t *testing.T) { gatewayError(t, err, codes.Unauthenticated, 401) })
	}
}

func TestP4_RateLimit(t *testing.T) {
	s := gatewaySuite(t)
	svc := s.svc("p4")
	s.start(t, svc)
	s.apply(t, rules{"routes": []route{{
		"name":    "p4",
		"match":   map[string]any{"service": svc},
		"plugins": []any{map[string]any{"name": "rate-limit", "config": map[string]any{"rate": 1, "burst": 0, "key": "client_ip"}}},
	}}})
	m := path(svc, "TestService", "UnaryCall")

	// a fresh bucket admits one call; wait out any token spent by readiness checks
	time.Sleep(1100 * time.Millisecond)
	s.call(t, m)
	_, err := s.gw.call(context.Background(), m, nil)
	gatewayError(t, err, codes.ResourceExhausted, 429)
}

func TestP5_InvalidRulesKeepPrevious(t *testing.T) {
	s := gatewaySuite(t)
	svc := s.svc("p5")
	s.start(t, svc)
	keep := path(s.svc("p5-keep"), "TestService", "UnaryCall")
	fresh := path(s.svc("p5-new"), "TestService", "UnaryCall")
	s.apply(t, rules{"routes": []route{{"name": "p5-keep", "match": map[string]any{"method": keep}, "upstream": map[string]any{"service": svc}}}})
	s.call(t, keep)

	// a typo'd plugin name: the whole document must be refused
	bad := rules{
		"version": 1,
		"global":  map[string]any{"plugins": []any{map[string]any{"name": "ip-restrction", "config": map[string]any{"deny": []string{"0.0.0.0/0"}}}}},
		"routes":  []route{{"name": "p5-new", "match": map[string]any{"method": fresh}, "upstream": map[string]any{"service": svc}}},
	}
	if s.validate(bad) == nil {
		t.Fatal("suite bug: the invalid document passes the schema")
	}
	s.write(t, bad)
	time.Sleep(propagation + grace)

	if _, err := s.gw.call(context.Background(), keep, nil); err != nil {
		t.Errorf("previous rules dropped after an invalid update: %s", failureOf(err))
	}
	if _, err := s.gw.call(context.Background(), fresh, nil); err == nil {
		t.Error("route from the invalid document is live")
	}
}

func TestC1_HotReloadUnderLoad(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("c1")
	m := path(svc, "TestService", "UnaryCall")
	n1, n2 := s.start(t, svc), s.start(t, svc)
	s.awaitNodes(t, m, n1, n2)

	l := s.startLoad(m)
	for i := 0; i < 3; i++ {
		took := s.apply(t, rules{"routes": []route{{"name": fmt.Sprintf("c1-%d", i), "match": map[string]any{"service": svc}}}})
		if took > propagation {
			t.Errorf("reload %d took %s, SPEC 10 allows %s", i, took, propagation)
		}
	}
	l.stop()
	l.assertClean(t)
}

func TestS1_Streaming(t *testing.T) {
	s := gatewaySuite(t)
	s.reset(t)
	svc := s.svc("s1")
	s.start(t, svc)
	s.awaitNodes(t, path(svc, "TestService", "UnaryCall"))

	got, err := s.gw.serverStream(context.Background(), path(svc, "TestService", "StreamingOutputCall"), 3)
	if err != nil || len(got) != 3 {
		t.Errorf("server streaming: got %d replies, err %v; want 3", len(got), err)
	}
	got, err = s.gw.bidiStream(context.Background(), path(svc, "TestService", "FullDuplexCall"), 3)
	if err != nil || len(got) != 3 {
		t.Errorf("bidirectional streaming: got %d replies, err %v; want 3", len(got), err)
	}
}
