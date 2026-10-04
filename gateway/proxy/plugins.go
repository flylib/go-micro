package proxy

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/grpc/metadata"
)

// call is what plugins see of an inbound call. Plugins run before node
// selection (SPEC 9.4) and may set account.
type call struct {
	ctx      context.Context
	method   string
	md       metadata.MD // inbound
	clientIP string
	account  string // set by jwt-auth
	trace    string // effective traceparent, for the access log
}

// plugin checks a call; a non-nil error is a gateway error that ends it.
type plugin interface {
	check(c *call) error
}

func buildPlugins(specs []PluginSpec) ([]plugin, error) {
	out := make([]plugin, 0, len(specs))
	for _, s := range specs {
		p, err := newPlugin(s)
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %w", s.Name, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func newPlugin(s PluginSpec) (plugin, error) {
	switch s.Name {
	case "ip-restriction":
		return newIPRestriction(s.Config)
	case "jwt-auth":
		return newJWTAuth(s.Config)
	case "rate-limit":
		return newRateLimit(s.Config)
	}
	// x- plugins pass the schema but are implementation-specific: a gateway
	// that does not know one must refuse the rules (SPEC 9.4)
	return nil, errors.New("unknown plugin")
}

// ip-restriction (SPEC 9.5): deny first, then allow if allow is set.

type ipRestriction struct {
	allow, deny []netip.Prefix
}

func newIPRestriction(raw json.RawMessage) (plugin, error) {
	var cfg struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	p := &ipRestriction{}
	var err error
	if p.allow, err = parsePrefixes(cfg.Allow); err != nil {
		return nil, err
	}
	if p.deny, err = parsePrefixes(cfg.Deny); err != nil {
		return nil, err
	}
	return p, nil
}

func parsePrefixes(in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, err
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// ParsePrefixes parses a CIDR or IP list, e.g. MICRO_GATEWAY_TRUSTED_PROXIES.
func ParsePrefixes(in []string) ([]netip.Prefix, error) { return parsePrefixes(in) }

func (p *ipRestriction) check(c *call) error {
	addr, err := netip.ParseAddr(c.clientIP)
	if err != nil {
		return errForbidden("client address unknown")
	}
	addr = addr.Unmap()
	for _, d := range p.deny {
		if d.Contains(addr) {
			return errForbidden("client address denied")
		}
	}
	if len(p.allow) == 0 {
		return nil
	}
	for _, a := range p.allow {
		if a.Contains(addr) {
			return nil
		}
	}
	return errForbidden("client address not allowed")
}

// jwt-auth (SPEC 9.5): RS256 tokens as issued by auth/jwt.

const jwtLeeway = 30 * time.Second

type jwtAuth struct {
	key    *rsa.PublicKey
	scopes []string
	now    func() time.Time
}

func newJWTAuth(raw json.RawMessage) (plugin, error) {
	var cfg struct {
		PublicKey string   `json:"public_key"`
		Scopes    []string `json:"scopes"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	// same encoding as auth/jwt/token.WithPublicKey: base64 of PEM
	pemBytes, err := base64.StdEncoding.DecodeString(cfg.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("public_key: not base64: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("public_key: not PEM")
	}
	key, err := parseRSAPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("public_key: %w", err)
	}
	return &jwtAuth{key: key, scopes: cfg.Scopes, now: time.Now}, nil
}

func parseRSAPublicKey(der []byte) (*rsa.PublicKey, error) {
	if k, err := x509.ParsePKIXPublicKey(der); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("not an RSA key")
	}
	return x509.ParsePKCS1PublicKey(der)
}

type jwtClaims struct {
	Sub    string   `json:"sub"`
	Exp    *int64   `json:"exp"`
	Nbf    *int64   `json:"nbf"`
	Scopes []string `json:"scopes"`
}

func (p *jwtAuth) check(c *call) error {
	auth := first(c.md, hdrAuthorization)
	tok, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || tok == "" {
		return errUnauthenticated("missing bearer token")
	}
	claims, err := p.verify(tok)
	if err != nil {
		return errUnauthenticated("invalid token: " + err.Error())
	}
	if len(p.scopes) > 0 && !anyScope(claims.Scopes, p.scopes) {
		return errForbidden("token lacks a required scope")
	}
	c.account = claims.Sub
	return nil
}

func (p *jwtAuth) verify(tok string) (*jwtClaims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed")
	}
	var hdr struct {
		Alg string `json:"alg"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return nil, err
	}
	// RS256 only: accepting the alg the token names is what enables
	// HS256-with-public-key forgeries
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("alg %q not allowed", hdr.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(p.key, crypto.SHA256, sum[:], sig); err != nil {
		return nil, errors.New("bad signature")
	}
	var cl jwtClaims
	if err := decodeSegment(parts[1], &cl); err != nil {
		return nil, err
	}
	now := p.now()
	if cl.Exp == nil || now.After(time.Unix(*cl.Exp, 0).Add(jwtLeeway)) {
		return nil, errors.New("expired")
	}
	if cl.Nbf != nil && now.Add(jwtLeeway).Before(time.Unix(*cl.Nbf, 0)) {
		return nil, errors.New("not yet valid")
	}
	return &cl, nil
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return errors.New("malformed segment")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errors.New("malformed segment")
	}
	return nil
}

func anyScope(have, want []string) bool {
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

// rate-limit (SPEC 9.5): token bucket per key, capacity 1+burst, local to
// this gateway. Buckets reset when the rules are reloaded.

const limiterIdle = 10 * time.Minute

type rateLimit struct {
	limit rate.Limit
	burst int
	key   string

	mu       sync.Mutex
	buckets  map[string]*bucket
	lastScan time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRateLimit(raw json.RawMessage) (plugin, error) {
	var cfg struct {
		Rate  float64 `json:"rate"`
		Burst int     `json:"burst"`
		Key   string  `json:"key"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return &rateLimit{
		limit:    rate.Limit(cfg.Rate),
		burst:    1 + cfg.Burst,
		key:      cfg.Key,
		buckets:  map[string]*bucket{},
		lastScan: time.Now(),
	}, nil
}

func (p *rateLimit) keyOf(c *call) string {
	switch {
	case p.key == "account" && c.account != "":
		return "account:" + c.account
	case strings.HasPrefix(p.key, "header:"):
		if v := first(c.md, strings.ToLower(strings.TrimPrefix(p.key, "header:"))); v != "" {
			return "header:" + v
		}
	}
	return "ip:" + c.clientIP
}

func (p *rateLimit) check(c *call) error {
	key := p.keyOf(c)
	now := time.Now()

	p.mu.Lock()
	b, ok := p.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(p.limit, p.burst)}
		p.buckets[key] = b
	}
	b.seen = now
	// drop idle buckets now and then so one-off keys do not accumulate
	if now.Sub(p.lastScan) > limiterIdle {
		for k, v := range p.buckets {
			if now.Sub(v.seen) > limiterIdle {
				delete(p.buckets, k)
			}
		}
		p.lastScan = now
	}
	p.mu.Unlock()

	if !b.lim.AllowN(now, 1) {
		return errRateLimited()
	}
	return nil
}
