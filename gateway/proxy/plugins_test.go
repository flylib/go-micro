package proxy

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net"
	"net/netip"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func code(err error) codes.Code { return status.Code(err) }

func TestIPRestriction(t *testing.T) {
	p, err := newIPRestriction([]byte(`{"allow":["10.0.0.0/8","2001:db8::/32"],"deny":["10.1.0.0/16","10.2.3.4"]}`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]codes.Code{
		"10.9.9.9":        codes.OK,
		"10.1.2.3":        codes.PermissionDenied, // deny beats allow
		"10.2.3.4":        codes.PermissionDenied, // bare IP
		"192.168.1.1":     codes.PermissionDenied, // not in allow
		"2001:db8::1":     codes.OK,
		"::ffff:10.9.9.9": codes.OK, // IPv4-mapped
		"garbage":         codes.PermissionDenied,
	}
	for ip, want := range cases {
		if got := code(p.check(&call{clientIP: ip})); got != want {
			t.Errorf("%s: %s, want %s", ip, got, want)
		}
	}
}

type signer struct {
	key *rsa.PrivateKey
	pub string // auth/jwt encoding: base64 of PEM
}

func newSigner(t *testing.T) signer {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return signer{key: k, pub: base64.StdEncoding.EncodeToString(pemBytes)}
}

func seg(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s signer) rs256(t *testing.T, claims map[string]any) string {
	in := seg(t, map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + seg(t, claims)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func bearer(tok string) metadata.MD { return metadata.Pairs("authorization", "Bearer "+tok) }

func TestJWTAuth(t *testing.T) {
	s := newSigner(t)
	pl, err := newJWTAuth([]byte(`{"public_key":"` + s.pub + `","scopes":["orders","admin"]}`))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	exp := now.Add(time.Minute).Unix()

	c := &call{md: bearer(s.rs256(t, map[string]any{"sub": "u1", "exp": exp, "scopes": []string{"orders"}}))}
	if err := pl.check(c); err != nil || c.account != "u1" {
		t.Fatalf("valid token: err %v account %q", err, c.account)
	}

	pub, _ := base64.StdEncoding.DecodeString(s.pub)
	hs := func(secret []byte) string {
		in := seg(t, map[string]string{"alg": "HS256"}) + "." + seg(t, map[string]any{"sub": "x", "exp": exp})
		m := hmac.New(sha256.New, secret)
		m.Write([]byte(in))
		return in + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
	}
	cases := map[string]struct {
		md   metadata.MD
		want codes.Code
	}{
		"no token":         {metadata.MD{}, codes.Unauthenticated},
		"not bearer":       {metadata.Pairs("authorization", "Basic x"), codes.Unauthenticated},
		"garbage":          {bearer("a.b.c"), codes.Unauthenticated},
		"expired":          {bearer(s.rs256(t, map[string]any{"sub": "u", "exp": now.Add(-time.Minute).Unix(), "scopes": []string{"orders"}})), codes.Unauthenticated},
		"within leeway":    {bearer(s.rs256(t, map[string]any{"sub": "u", "exp": now.Add(-10 * time.Second).Unix(), "scopes": []string{"orders"}})), codes.OK},
		"no exp":           {bearer(s.rs256(t, map[string]any{"sub": "u", "scopes": []string{"orders"}})), codes.Unauthenticated},
		"wrong scope":      {bearer(s.rs256(t, map[string]any{"sub": "u", "exp": exp, "scopes": []string{"billing"}})), codes.PermissionDenied},
		"hs256 pem secret": {bearer(hs(pub)), codes.Unauthenticated},
		"hs256 b64 secret": {bearer(hs([]byte(s.pub))), codes.Unauthenticated},
		"other key":        {bearer(newSigner(t).rs256(t, map[string]any{"sub": "u", "exp": exp, "scopes": []string{"orders"}})), codes.Unauthenticated},
	}
	for name, tc := range cases {
		if got := code(pl.check(&call{md: tc.md})); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestRateLimitBucket(t *testing.T) {
	pl, err := newRateLimit([]byte(`{"rate":1,"burst":1,"key":"client_ip"}`))
	if err != nil {
		t.Fatal(err)
	}
	a, b := &call{clientIP: "1.1.1.1"}, &call{clientIP: "2.2.2.2"}
	// capacity is 1+burst = 2
	for i, want := range []codes.Code{codes.OK, codes.OK, codes.ResourceExhausted} {
		if got := code(pl.check(a)); got != want {
			t.Errorf("call %d: %s, want %s", i, got, want)
		}
	}
	if err := pl.check(b); err != nil {
		t.Errorf("keys share a bucket: %v", err)
	}
}

func TestRateLimitKeys(t *testing.T) {
	pl, _ := newRateLimit([]byte(`{"rate":1,"key":"account"}`))
	rl := pl.(*rateLimit)
	if k := rl.keyOf(&call{clientIP: "1.1.1.1"}); k != "ip:1.1.1.1" {
		t.Errorf("unauthenticated account key = %q", k)
	}
	if k := rl.keyOf(&call{clientIP: "1.1.1.1", account: "u"}); k != "account:u" {
		t.Errorf("account key = %q", k)
	}
	pl, _ = newRateLimit([]byte(`{"rate":1,"key":"header:X-Tenant"}`))
	if k := pl.(*rateLimit).keyOf(&call{md: metadata.Pairs("x-tenant", "t1")}); k != "header:t1" {
		t.Errorf("header key = %q", k)
	}
}

func TestOutgoingMetadata(t *testing.T) {
	in := metadata.Pairs(
		"x-app", "1",
		"micro-gateway-account", "spoofed",
		"content-type", "application/grpc",
		"x-forwarded-for", "9.9.9.9",
		"traceparent", "00-00000000000000000000000000000000-0000000000000000-01",
	)
	out := outgoingMetadata(in, "1.2.3.4")
	if first(out, "x-app") != "1" || len(out.Get("micro-gateway-account")) != 0 || len(out.Get("content-type")) != 0 {
		t.Errorf("filtering wrong: %v", out)
	}
	if got := first(out, "x-forwarded-for"); got != "9.9.9.9, 1.2.3.4" {
		t.Errorf("x-forwarded-for = %q", got)
	}
	if tp := first(out, "traceparent"); !validTraceparent(tp) {
		t.Errorf("all-zero traceparent not replaced: %q", tp)
	}

	keep := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if tp := first(outgoingMetadata(metadata.Pairs("traceparent", keep), "1.2.3.4"), "traceparent"); tp != keep {
		t.Errorf("valid traceparent replaced: %q", tp)
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	peer := &net.TCPAddr{IP: net.ParseIP("10.0.0.5"), Port: 1}
	md := metadata.Pairs("x-forwarded-for", "6.6.6.6, 7.7.7.7, 10.0.0.9")
	if got := clientIP(hostOf(peer), md, trusted); got != "7.7.7.7" {
		t.Errorf("behind trusted proxies: %q, want 7.7.7.7", got)
	}
	if got := clientIP("8.8.8.8", md, trusted); got != "8.8.8.8" {
		t.Errorf("untrusted peer's x-forwarded-for believed: %q", got)
	}
}

func TestEndpointOf(t *testing.T) {
	for in, want := range map[string]string{
		"/greeter.Greeter/Hello": "Greeter.Hello",
		"/a.b.c.Greeter/Hello":   "Greeter.Hello",
		"/Greeter/Hello":         "Greeter.Hello",
		"/malformed":             "",
	} {
		if got := endpointOf(in); got != want {
			t.Errorf("endpointOf(%s) = %q, want %q", in, got, want)
		}
	}
}
