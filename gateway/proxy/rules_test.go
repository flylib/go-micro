package proxy

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestSchemaInSync(t *testing.T) {
	canonical, err := os.ReadFile("../rules.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, schemaJSON) {
		t.Fatal("schema.json differs from gateway/rules.schema.json: cp ../rules.schema.json schema.json")
	}
}

func TestExampleRulesParse(t *testing.T) {
	b, err := os.ReadFile("../rules.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// the example's public key is a placeholder, so plugin building fails;
	// everything before it (schema, structure) must pass
	_, err = parseRules(b, "nacos")
	if err == nil || !strings.Contains(err.Error(), "jwt-auth") {
		t.Fatalf("want only the placeholder key rejected, got %v", err)
	}
}

func mustRules(t *testing.T, doc string) *ruleSet {
	t.Helper()
	rs, err := parseRules([]byte(doc), "nacos")
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestMatchPrecedence(t *testing.T) {
	rs := mustRules(t, `
version: 1
routes:
  - {name: by-service, match: {service: y}}
  - {name: short, match: {prefix: /y.}}
  - {name: long, match: {prefix: /y.Alt/}}
  - {name: exact, match: {method: /y.Svc/Echo}}
`)
	cases := map[string]string{
		"/y.Svc/Echo": "exact",
		"/y.Alt/Call": "long",
		"/y.Svc/Call": "short",
		"/z.Svc/Call": "", // convention route
	}
	for path, want := range cases {
		rt := rs.match(path, strings.SplitN(path[1:], ".", 2)[0])
		if rt == nil || rt.name != want {
			t.Errorf("%s matched %v, want %q", path, rt, want)
		}
	}
}

func TestServiceRouteWithoutPrefixes(t *testing.T) {
	rs := mustRules(t, `
version: 1
routes: [{name: s, match: {service: y}, upstream: {service: real}}]
`)
	rt := rs.match("/y.Svc/Call", "y")
	if rt == nil || rt.name != "s" || rt.service != "real" {
		t.Fatalf("got %+v", rt)
	}
}

func TestConventionOff(t *testing.T) {
	rs := mustRules(t, "version: 1\ndefaults: {convention: false}\n")
	if rs.match("/x.Svc/Call", "x") != nil {
		t.Fatal("unmatched call routed with convention off")
	}
}

func TestDefaultsResolve(t *testing.T) {
	rs := mustRules(t, `
version: 1
defaults: {timeout: {connect: 2s}, retries: 4, selector: {strategy: random}}
routes:
  - {name: a, match: {service: a}, upstream: {timeout: {read: 30s}}}
  - {name: b, match: {service: b}, upstream: {retries: 0, selector: {version_weights: {v1: 1}}}}
`)
	a := rs.services["a"]
	if a.connect != 2*time.Second || a.read != 30*time.Second || a.send != defaultSend || a.retries != 4 || a.selector.Strategy != "random" {
		t.Errorf("route a: %+v", a)
	}
	b := rs.services["b"]
	if b.retries != 0 || b.selector.Strategy != "roundrobin" || b.selector.VersionWeights["v1"] != 1 {
		t.Errorf("route b: %+v", b)
	}
	if c := rs.convention; c.connect != 2*time.Second || c.retries != 4 {
		t.Errorf("convention route ignores defaults: %+v", c)
	}
}

func TestRejectedRules(t *testing.T) {
	cases := map[string]string{
		"unknown plugin":   "version: 1\nglobal: {plugins: [{name: ip-restrction, config: {deny: [1.2.3.4]}}]}",
		"x- plugin":        "version: 1\nglobal: {plugins: [{name: x-lua, config: {}}]}",
		"duplicate name":   "version: 1\nroutes: [{name: a, match: {service: a}}, {name: a, match: {service: b}}]",
		"duplicate method": "version: 1\nroutes: [{name: a, match: {method: /a.B/C}}, {name: b, match: {method: /a.B/C}}]",
		"endpoint filter":  "version: 1\nroutes: [{name: a, match: {service: a}, upstream: {filters: {endpoint: true}}}]",
		"bad version":      "version: 2",
		"not yaml":         "version: [",
	}
	for name, doc := range cases {
		if _, err := parseRules([]byte(doc), "nacos"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// the endpoint filter is fine where endpoint lists exist
	if _, err := parseRules([]byte(cases["endpoint filter"]), "etcd"); err != nil {
		t.Errorf("endpoint filter with etcd: %v", err)
	}
}
