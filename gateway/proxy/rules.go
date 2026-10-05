package proxy

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// schemaJSON is a copy of gateway/rules.schema.json; TestSchemaInSync
// fails when the two drift.
//
//go:embed schema.json
var schemaJSON []byte

var rulesSchema = mustCompileSchema()

func mustCompileSchema() *jsonschema.Schema {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		panic(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("rules.schema.json", doc); err != nil {
		panic(err)
	}
	return c.MustCompile("rules.schema.json")
}

// Built-in defaults (SPEC 6), used where neither the route nor
// rules.defaults sets a value.
const (
	defaultConnect = time.Second
	defaultSend    = 10 * time.Second
	defaultRead    = 10 * time.Second
	defaultRetries = 2
)

// Rules mirrors gateway/rules.schema.json (SPEC 9).
type Rules struct {
	Version   int            `json:"version"`
	Defaults  Defaults       `json:"defaults"`
	Global    Global         `json:"global"`
	Routes    []RouteSpec    `json:"routes"`
	HTTPRules []HTTPRuleSpec `json:"http_rules"`
	WebSocket *WebSocketSpec `json:"websocket"`
}

// WebSocketSpec configures the WebSocket entry (SPEC 2.3, 9.7).
type WebSocketSpec struct {
	Plugins  []PluginSpec `json:"plugins"`
	Topics   []string     `json:"topics"`
	MaxCalls int          `json:"max_calls"`
}

type Defaults struct {
	Convention *bool         `json:"convention"`
	Timeout    TimeoutSpec   `json:"timeout"`
	Retries    *int          `json:"retries"`
	Selector   *SelectorSpec `json:"selector"`
}

type Global struct {
	Plugins []PluginSpec `json:"plugins"`
}

type TimeoutSpec struct {
	Connect string `json:"connect"`
	Send    string `json:"send"`
	Read    string `json:"read"`
}

type SelectorSpec struct {
	Strategy       string         `json:"strategy"`
	VersionWeights map[string]int `json:"version_weights"`
}

type FilterSpec struct {
	Version  string            `json:"version"`
	Labels   map[string]string `json:"labels"`
	Endpoint bool              `json:"endpoint"`
}

type UpstreamSpec struct {
	Service  string        `json:"service"`
	Selector *SelectorSpec `json:"selector"`
	Filters  FilterSpec    `json:"filters"`
	Timeout  TimeoutSpec   `json:"timeout"`
	Retries  *int          `json:"retries"`
}

type MatchSpec struct {
	Method  string `json:"method"`
	Prefix  string `json:"prefix"`
	Service string `json:"service"`
}

type RouteSpec struct {
	Name     string       `json:"name"`
	Match    MatchSpec    `json:"match"`
	Upstream UpstreamSpec `json:"upstream"`
	Plugins  []PluginSpec `json:"plugins"`
}

type PluginSpec struct {
	Name   string          `json:"name"`
	Config json.RawMessage `json:"config"`
}

// route is a compiled route: every value resolved against the defaults.
type route struct {
	name     string // empty for convention routing
	service  string // upstream service; empty means the one derived from the path
	selector SelectorSpec
	filters  FilterSpec
	connect  time.Duration
	send     time.Duration
	read     time.Duration
	retries  int
	plugins  []plugin
	rr       atomic.Uint64 // roundrobin position
}

// ruleSet is a validated, compiled rules document. It is immutable once
// built and swapped in atomically on reload (SPEC 10).
type ruleSet struct {
	methods    map[string]*route
	prefixes   []prefixRoute // longest first
	services   map[string]*route
	convention *route // nil when defaults.convention is false
	global     []plugin
	httpRules  []*httpRule     // most specific first (SPEC 2.2)
	forwarded  map[string]bool // metadata keys set by jwt-auth forward_claims
	ws         *wsRules        // nil: no WebSocket entry
}

type prefixRoute struct {
	prefix string
	route  *route
}

// match picks the route for a call (SPEC 3.2): exact method, then longest
// prefix, then the derived service, then convention routing.
func (rs *ruleSet) match(method, derived string) *route {
	if r, ok := rs.methods[method]; ok {
		return r
	}
	for _, p := range rs.prefixes {
		if strings.HasPrefix(method, p.prefix) {
			return p.route
		}
	}
	if r, ok := rs.services[derived]; ok {
		return r
	}
	return rs.convention
}

// parseRules validates and compiles a rules document (YAML or JSON).
// registryName gates registry-dependent features (SPEC 5.2).
func parseRules(data []byte, registryName string) (*ruleSet, error) {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	// round-trip through JSON: the schema validator and the typed decode
	// both work on JSON values
	js, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(js))
	if err != nil {
		return nil, fmt.Errorf("parse rules: %w", err)
	}
	if err := rulesSchema.Validate(inst); err != nil {
		return nil, fmt.Errorf("rules violate schema: %w", err)
	}
	var r Rules
	if err := json.Unmarshal(js, &r); err != nil {
		return nil, fmt.Errorf("decode rules: %w", err)
	}
	return compile(r, registryName)
}

func compile(r Rules, registryName string) (*ruleSet, error) {
	base, err := defaultsRoute(r.Defaults)
	if err != nil {
		return nil, err
	}
	rs := &ruleSet{methods: map[string]*route{}, services: map[string]*route{}}
	if r.Defaults.Convention == nil || *r.Defaults.Convention {
		rs.convention = base
	}
	if rs.global, err = buildPlugins(r.Global.Plugins); err != nil {
		return nil, fmt.Errorf("global: %w", err)
	}

	names := map[string]bool{}
	for _, spec := range r.Routes {
		if names[spec.Name] {
			return nil, fmt.Errorf("route %q: duplicate name", spec.Name)
		}
		names[spec.Name] = true
		rt, err := compileRoute(spec, base, registryName)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", spec.Name, err)
		}
		switch m := spec.Match; {
		case m.Method != "":
			if _, dup := rs.methods[m.Method]; dup {
				return nil, fmt.Errorf("route %q: method %s already matched by another route", spec.Name, m.Method)
			}
			rs.methods[m.Method] = rt
		case m.Prefix != "":
			for _, p := range rs.prefixes {
				if p.prefix == m.Prefix {
					return nil, fmt.Errorf("route %q: prefix %s already matched by another route", spec.Name, m.Prefix)
				}
			}
			rs.prefixes = append(rs.prefixes, prefixRoute{m.Prefix, rt})
		default:
			if _, dup := rs.services[m.Service]; dup {
				return nil, fmt.Errorf("route %q: service %s already matched by another route", spec.Name, m.Service)
			}
			rs.services[m.Service] = rt
		}
	}
	sort.SliceStable(rs.prefixes, func(i, j int) bool {
		return len(rs.prefixes[i].prefix) > len(rs.prefixes[j].prefix)
	})
	if rs.httpRules, err = compileHTTPRules(r.HTTPRules); err != nil {
		return nil, err
	}
	if r.WebSocket != nil {
		if rs.ws, err = compileWS(*r.WebSocket); err != nil {
			return nil, fmt.Errorf("websocket: %w", err)
		}
	}

	// keys any jwt-auth forwards are stripped from every inbound call,
	// whatever its route, so clients cannot set them (SPEC 9.5)
	rs.forwarded = map[string]bool{}
	all := append([]plugin{}, rs.global...)
	for _, rt := range rs.routeList() {
		all = append(all, rt.plugins...)
	}
	if rs.ws != nil {
		all = append(all, rs.ws.plugins...)
	}
	for _, pl := range all {
		if ja, ok := pl.(*jwtAuth); ok {
			for k := range ja.forward {
				rs.forwarded[k] = true
			}
		}
	}
	return rs, nil
}

// routeList returns every compiled route once.
func (rs *ruleSet) routeList() []*route {
	var out []*route
	for _, rt := range rs.methods {
		out = append(out, rt)
	}
	for _, p := range rs.prefixes {
		out = append(out, p.route)
	}
	for _, rt := range rs.services {
		out = append(out, rt)
	}
	return out
}

func defaultsRoute(d Defaults) (*route, error) {
	rt := &route{
		selector: SelectorSpec{Strategy: "roundrobin"},
		connect:  defaultConnect,
		send:     defaultSend,
		read:     defaultRead,
		retries:  defaultRetries,
	}
	if d.Selector != nil {
		rt.selector = normalizeSelector(*d.Selector)
	}
	if d.Retries != nil {
		rt.retries = *d.Retries
	}
	if err := applyTimeouts(rt, d.Timeout); err != nil {
		return nil, fmt.Errorf("defaults: %w", err)
	}
	return rt, nil
}

func compileRoute(spec RouteSpec, base *route, registryName string) (*route, error) {
	rt := &route{
		name:     spec.Name,
		service:  spec.Upstream.Service,
		selector: base.selector,
		filters:  spec.Upstream.Filters,
		connect:  base.connect,
		send:     base.send,
		read:     base.read,
		retries:  base.retries,
	}
	if spec.Upstream.Selector != nil {
		rt.selector = normalizeSelector(*spec.Upstream.Selector)
	}
	if spec.Upstream.Retries != nil {
		rt.retries = *spec.Upstream.Retries
	}
	if err := applyTimeouts(rt, spec.Upstream.Timeout); err != nil {
		return nil, err
	}
	// only etcd stores endpoint lists; elsewhere the filter would pass
	// every node, so refuse it instead (SPEC 5.2)
	if rt.filters.Endpoint && registryName != "etcd" {
		return nil, fmt.Errorf("filters.endpoint needs the etcd registry, have %q", registryName)
	}
	var err error
	if rt.plugins, err = buildPlugins(spec.Plugins); err != nil {
		return nil, err
	}
	return rt, nil
}

func normalizeSelector(s SelectorSpec) SelectorSpec {
	if s.Strategy == "" {
		s.Strategy = "roundrobin"
	}
	return s
}

func applyTimeouts(rt *route, t TimeoutSpec) error {
	for _, f := range []struct {
		val string
		dst *time.Duration
		key string
	}{{t.Connect, &rt.connect, "connect"}, {t.Send, &rt.send, "send"}, {t.Read, &rt.read, "read"}} {
		if f.val == "" {
			continue
		}
		d, err := time.ParseDuration(f.val)
		if err != nil {
			return fmt.Errorf("timeout.%s: %w", f.key, err)
		}
		if d <= 0 {
			return errors.New("timeout." + f.key + " must be positive")
		}
		*f.dst = d
	}
	return nil
}
