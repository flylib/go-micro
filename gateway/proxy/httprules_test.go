package proxy

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"

	"github.com/flylib/go-micro/registry"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

func mustTemplate(t *testing.T, tmpl string) *httpRule {
	t.Helper()
	hr, err := parseTemplate(tmpl)
	if err != nil {
		t.Fatalf("%s: %v", tmpl, err)
	}
	return hr
}

func TestTemplateMatch(t *testing.T) {
	cases := []struct {
		tmpl, path string
		vars       map[string]string // nil: no match
	}{
		{"/v1/users/{id}", "/v1/users/42", map[string]string{"id": "42"}},
		{"/v1/users/{id}", "/v1/users/42/x", nil},
		{"/v1/users/{id}", "/v1/users/", nil},
		{"/v1/users/{user.id}/books/{book}", "/v1/users/7/books/b1", map[string]string{"user.id": "7", "book": "b1"}},
		{"/v1/{name=projects/*/books/*}", "/v1/projects/p1/books/b2", map[string]string{"name": "projects/p1/books/b2"}},
		{"/v1/{name=projects/*/books/*}", "/v1/shelves/p1/books/b2", nil},
		{"/v1/files/{path=**}", "/v1/files/a/b/c.txt", map[string]string{"path": "a/b/c.txt"}},
		{"/v1/files/{path=**}", "/v1/files", map[string]string{"path": ""}},
		{"/v1/*/x", "/v1/anything/x", map[string]string{}},
		{"/v1/users/{id}:activate", "/v1/users/9:activate", map[string]string{"id": "9"}},
		{"/v1/users/{id}:activate", "/v1/users/9", nil},
		// raw segments are matched, values decoded
		{"/v1/users/{id}", "/v1/users/a%2Fb", map[string]string{"id": "a/b"}},
		{"/v1/users/{id}", "/v1/users/caf%C3%A9", map[string]string{"id": "café"}},
	}
	for _, tc := range cases {
		vars, ok := mustTemplate(t, tc.tmpl).match(tc.path)
		if (tc.vars == nil) != !ok {
			t.Errorf("%s ~ %s: matched=%v", tc.tmpl, tc.path, ok)
			continue
		}
		if ok && !reflect.DeepEqual(vars, tc.vars) {
			t.Errorf("%s ~ %s: vars %v, want %v", tc.tmpl, tc.path, vars, tc.vars)
		}
	}
}

func TestTemplateRejected(t *testing.T) {
	for _, bad := range []string{"v1/x", "/v1//x", "/v1/x/", "/v1/{id", "/v1/**/x", "/v1/{}", "/v1/{a=**}/x", "/v1/x:", "/v1/a=b"} {
		if _, err := parseTemplate(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestHTTPRuleSpecificity(t *testing.T) {
	rules, err := compileHTTPRules([]HTTPRuleSpec{
		{Method: "GET", Path: "/v1/**", Target: "/s.S/All"},
		{Method: "GET", Path: "/v1/items/{id}", Target: "/s.S/Get"},
		{Method: "GET", Path: "/v1/items/special", Target: "/s.S/Special"},
		{Method: "POST", Path: "/v1/items/{id}", Target: "/s.S/Update"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rs := &ruleSet{httpRules: rules}
	for path, want := range map[string]string{
		"/v1/items/special": "/s.S/Special",
		"/v1/items/42":      "/s.S/Get",
		"/v1/other/a/b":     "/s.S/All",
	} {
		if hr, _ := rs.matchHTTP("GET", path); hr == nil || hr.target != want {
			t.Errorf("GET %s -> %v, want %s", path, hr, want)
		}
	}
	if hr, _ := rs.matchHTTP("POST", "/v1/items/1"); hr == nil || hr.target != "/s.S/Update" {
		t.Errorf("method not matched: %v", hr)
	}
	if hr, _ := rs.matchHTTP("DELETE", "/v1/items/1"); hr != nil {
		t.Errorf("DELETE matched %v", hr)
	}
	if _, err := compileHTTPRules([]HTTPRuleSpec{
		{Method: "GET", Path: "/v1/x", Target: "/s.S/A"},
		{Method: "GET", Path: "/v1/x", Target: "/s.S/B"},
	}); err == nil {
		t.Error("duplicate method+path accepted")
	}
}

func buildJSON(t *testing.T, spec HTTPRuleSpec, path, rawQuery, body string) (map[string]any, error) {
	t.Helper()
	rules, err := compileHTTPRules([]HTTPRuleSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	vars, ok := rules[0].match(path)
	if !ok {
		t.Fatalf("%s does not match %s", path, spec.Path)
	}
	q, _ := url.ParseQuery(rawQuery)
	b, err := rules[0].build([]byte(body), vars, q)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m, nil
}

func TestBuildNoBody(t *testing.T) {
	spec := HTTPRuleSpec{Method: "GET", Path: "/v1/users/{id}", Target: "/u.U/Get",
		Params: map[string]string{"id": "string", "verbose": "bool", "tags": "repeated", "page.size": "number"}}
	m, err := buildJSON(t, spec, "/v1/users/42", "verbose=true&tags=a&page.size=10&id=999&unknown=x", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id":      "42", // path wins over the query
		"verbose": true,
		"tags":    []any{"a"},
		"page":    map[string]any{"size": "10"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %v\nwant %v", m, want)
	}
	if _, err := buildJSON(t, spec, "/v1/users/1", "verbose=yes", ""); errCode(err) != codes.InvalidArgument {
		t.Errorf("bad bool: %v", err)
	}
	if _, err := buildJSON(t, spec, "/v1/users/1", "", `{"x":1}`); errCode(err) != codes.InvalidArgument {
		t.Errorf("body on a no-body rule: %v", err)
	}
	if _, err := buildJSON(t, spec, "/v1/users/1", "", "{}"); err != nil {
		t.Errorf("empty object body refused: %v", err)
	}
}

func TestBuildWithoutParamsKeepsQuery(t *testing.T) {
	spec := HTTPRuleSpec{Method: "GET", Path: "/v1/x", Target: "/s.S/X"}
	m, err := buildJSON(t, spec, "/v1/x", "a=1&b.c=2&r=1&r=2", "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": "1", "b": map[string]any{"c": "2"}, "r": []any{"1", "2"}}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %v", m)
	}
}

func TestBuildStarBody(t *testing.T) {
	spec := HTTPRuleSpec{Method: "POST", Path: "/v1/users/{id}", Target: "/u.U/Update", Body: "*"}
	m, err := buildJSON(t, spec, "/v1/users/7", "ignored=1", `{"id":"from-body","name":"n","big":9007199254740993}`)
	if err != nil {
		t.Fatal(err)
	}
	if m["id"] != "7" || m["name"] != "n" || m["ignored"] != nil {
		t.Errorf("got %v", m)
	}
	// int64 beyond float64 precision survives
	b, _ := (&httpRule{body: "*"}).build([]byte(`{"big":9007199254740993}`), nil, nil)
	if string(b) != `{"big":9007199254740993}` {
		t.Errorf("number changed: %s", b)
	}
	if _, err := buildJSON(t, spec, "/v1/users/7", "", `[1]`); errCode(err) != codes.InvalidArgument {
		t.Errorf("array body: %v", err)
	}
}

func TestBuildFieldBody(t *testing.T) {
	spec := HTTPRuleSpec{Method: "POST", Path: "/v1/users/{user.id}/notes", Target: "/u.U/Note", Body: "note"}
	m, err := buildJSON(t, spec, "/v1/users/3/notes", "user.name=al", `{"text":"hi"}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"note": map[string]any{"text": "hi"},
		"user": map[string]any{"id": "3", "name": "al"},
	}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %v", m)
	}
}

func TestReplyResponseBody(t *testing.T) {
	hr := &httpRule{responseBody: "payload"}
	out, err := hr.reply([]byte(`{"payload":{"body":"YQ=="},"username":"u"}`))
	if err != nil || string(out) != `{"body":"YQ=="}` {
		t.Errorf("got %s %v", out, err)
	}
	out, _ = (&httpRule{responseBody: "missing"}).reply([]byte(`{"a":1}`))
	if string(out) != "null" {
		t.Errorf("unset field: %s", out)
	}
	out, _ = (&httpRule{}).reply([]byte(`{"a":1}`))
	if string(out) != `{"a":1}` {
		t.Errorf("whole message: %s", out)
	}
}

func TestForwardClaims(t *testing.T) {
	s := newSigner(t)
	rs := mustRules(t, `
version: 1
routes:
  - name: a
    match: {service: a}
    plugins: [{name: jwt-auth, config: {public_key: "`+s.pub+`", forward_claims: {user-id: sub, tenant: tid}}}]
`)
	if !rs.forwarded["user-id"] || !rs.forwarded["tenant"] {
		t.Fatalf("forwarded keys = %v", rs.forwarded)
	}
	pl := rs.services["a"].plugins[0]
	c := &call{md: bearer(s.rs256(t, map[string]any{"sub": "u-1", "tid": 7, "exp": 9999999999}))}
	if err := pl.check(c); err != nil {
		t.Fatal(err)
	}
	if c.forward["user-id"] != "u-1" || c.forward["tenant"] != "7" {
		t.Errorf("forward = %v", c.forward)
	}
}

func TestForwardedKeysStrippedOnEveryRoute(t *testing.T) {
	s := newSigner(t)
	gw, err := New(Registry(memoryRegistryWith(t, "b")))
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Stop()
	rs := mustRules(t, `
version: 1
routes:
  - name: a
    match: {service: a}
    plugins: [{name: jwt-auth, config: {public_key: "`+s.pub+`", forward_claims: {user-id: sub}}}]
`)
	gw.rules.Store(rs)
	// route b has no jwt-auth; a spoofed user-id must still be dropped
	c := &call{method: "/b.S/Call", md: metadata.Pairs("user-id", "spoofed"), clientIP: "1.2.3.4"}
	pc, err := gw.prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	if v := pc.out.Get("user-id"); len(v) != 0 {
		t.Errorf("spoofed user-id forwarded: %v", v)
	}
}

func TestHTTPRulesInRulesDocument(t *testing.T) {
	rs := mustRules(t, `
version: 1
http_rules:
  - {method: GET, path: "/v1/users/{id}", target: /u.U/Get, params: {verbose: bool}}
  - {method: POST, path: "/v1/users", target: /u.U/Create, body: "*", response_body: user}
`)
	if hr, vars := rs.matchHTTP("GET", "/v1/users/5"); hr == nil || vars["id"] != "5" || hr.params["verbose"] != "bool" {
		t.Errorf("GET rule: %v %v", hr, vars)
	}
	for name, doc := range map[string]string{
		"bad method":   "version: 1\nhttp_rules: [{method: HEAD, path: /x, target: /a.B/C}]",
		"bad target":   "version: 1\nhttp_rules: [{method: GET, path: /x, target: a.B/C}]",
		"bad template": "version: 1\nhttp_rules: [{method: GET, path: /x/**/y, target: /a.B/C}]",
		"bad type":     "version: 1\nhttp_rules: [{method: GET, path: /x, target: /a.B/C, params: {a: int}}]",
		"duplicate":    "version: 1\nhttp_rules: [{method: GET, path: /x, target: /a.B/C}, {method: GET, path: /x, target: /a.B/D}]",
	} {
		if _, err := parseRules([]byte(doc), "nacos"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func errCode(err error) codes.Code { return code(err) }

// memoryRegistryWith returns a memory registry holding one gRPC node of
// each named service.
func memoryRegistryWith(t *testing.T, services ...string) registry.Registry {
	t.Helper()
	reg := registry.NewMemoryRegistry()
	for _, s := range services {
		err := reg.Register(&registry.Service{Name: s, Nodes: []*registry.Node{
			{Id: s + "-1", Address: "127.0.0.1:1", Metadata: map[string]string{"protocol": "grpc"}},
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	return reg
}
