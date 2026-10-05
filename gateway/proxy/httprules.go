package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
)

// HTTP rules (SPEC 2.2): REST mappings after google.api.http. A rule's
// path template is matched against the raw request path; the request
// message is built from the body, the path variables and the query, and
// sent as an application/grpc+json call to the rule's target.

// HTTPRuleSpec mirrors one entry of http_rules (SPEC 9.6).
type HTTPRuleSpec struct {
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Target       string            `json:"target"`
	Body         string            `json:"body"`
	ResponseBody string            `json:"response_body"`
	Params       map[string]string `json:"params"`
}

type tokKind int

const (
	tokLiteral tokKind = iota
	tokStar            // one segment
	tokDStar           // the remaining segments, zero or more
)

// token is one matched element; variable >= 0 binds it to vars[variable].
type token struct {
	kind     tokKind
	literal  string
	variable int
}

type httpRule struct {
	method       string
	path         string
	target       string
	body         string
	responseBody string
	params       map[string]string
	tokens       []token
	verb         string
	vars         []string // field path per variable index
	literals     int      // specificity: literal segments
	dstars       int      // specificity: ** wildcards
	order        int
}

func compileHTTPRules(specs []HTTPRuleSpec) ([]*httpRule, error) {
	seen := map[string]bool{}
	out := make([]*httpRule, 0, len(specs))
	for i, s := range specs {
		key := s.Method + " " + s.Path
		if seen[key] {
			return nil, fmt.Errorf("http_rules: %s defined twice", key)
		}
		seen[key] = true
		hr, err := parseTemplate(s.Path)
		if err != nil {
			return nil, fmt.Errorf("http_rules: %s: %w", key, err)
		}
		hr.method, hr.path, hr.target = s.Method, s.Path, s.Target
		hr.body, hr.responseBody, hr.params = s.Body, s.ResponseBody, s.Params
		hr.order = i
		out = append(out, hr)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.literals != b.literals {
			return a.literals > b.literals
		}
		if a.dstars != b.dstars {
			return a.dstars < b.dstars
		}
		return a.order < b.order
	})
	return out, nil
}

// parseTemplate parses a google.api.http path template:
// "/" segments ["/" ...] [":" verb], a segment being a literal, "*",
// "**", "{field}" or "{field=pattern}".
func parseTemplate(t string) (*httpRule, error) {
	if !strings.HasPrefix(t, "/") {
		return nil, errors.New("template must start with /")
	}
	hr := &httpRule{}
	body := t[1:]
	// a verb follows the last ':' outside braces
	depth := 0
	for i := len(body) - 1; i >= 0; i-- {
		switch body[i] {
		case '}':
			depth++
		case '{':
			depth--
		case ':':
			if depth == 0 && !strings.Contains(body[i:], "/") {
				hr.verb = body[i+1:]
				body = body[:i]
				if hr.verb == "" {
					return nil, errors.New("empty verb")
				}
			}
		}
		if hr.verb != "" {
			break
		}
	}

	for len(body) > 0 || len(hr.tokens) == 0 {
		var seg string
		if strings.HasPrefix(body, "{") {
			end := strings.Index(body, "}")
			if end < 0 {
				return nil, errors.New("unclosed {")
			}
			seg, body = body[:end+1], body[end+1:]
		} else if i := strings.Index(body, "/"); i >= 0 {
			seg, body = body[:i], body[i:]
		} else {
			seg, body = body, ""
		}
		if err := hr.addSegment(seg); err != nil {
			return nil, err
		}
		if body == "" {
			break
		}
		if body[0] != '/' {
			return nil, fmt.Errorf("unexpected %q after a segment", body)
		}
		body = body[1:]
		if body == "" {
			return nil, errors.New("trailing /")
		}
	}

	for i, tk := range hr.tokens {
		if tk.kind == tokDStar && i != len(hr.tokens)-1 {
			return nil, errors.New("** must be the last element")
		}
	}
	return hr, nil
}

func (hr *httpRule) addSegment(seg string) error {
	switch {
	case seg == "":
		return errors.New("empty segment")
	case strings.HasPrefix(seg, "{"):
		inner := seg[1 : len(seg)-1]
		field, pattern, hasPattern := strings.Cut(inner, "=")
		if field == "" || strings.ContainsAny(field, "{}/*") {
			return fmt.Errorf("bad variable %q", seg)
		}
		v := len(hr.vars)
		hr.vars = append(hr.vars, field)
		if !hasPattern {
			pattern = "*"
		}
		for _, p := range strings.Split(pattern, "/") {
			tk, err := literalOrWildcard(p)
			if err != nil {
				return fmt.Errorf("variable %s: %w", field, err)
			}
			tk.variable = v
			hr.add(tk)
		}
	default:
		tk, err := literalOrWildcard(seg)
		if err != nil {
			return err
		}
		tk.variable = -1
		hr.add(tk)
	}
	return nil
}

func literalOrWildcard(s string) (token, error) {
	switch {
	case s == "*":
		return token{kind: tokStar}, nil
	case s == "**":
		return token{kind: tokDStar}, nil
	case s == "" || strings.ContainsAny(s, "{}*="):
		return token{}, fmt.Errorf("bad segment %q", s)
	}
	return token{kind: tokLiteral, literal: s}, nil
}

func (hr *httpRule) add(tk token) {
	switch tk.kind {
	case tokLiteral:
		hr.literals++
	case tokDStar:
		hr.dstars++
	}
	hr.tokens = append(hr.tokens, tk)
}

// match matches a raw (percent-encoded) request path and returns the
// decoded variable values by field path.
func (hr *httpRule) match(rawPath string) (map[string]string, bool) {
	p := strings.TrimPrefix(rawPath, "/")
	if hr.verb != "" {
		cut, ok := strings.CutSuffix(p, ":"+hr.verb)
		if !ok {
			return nil, false
		}
		p = cut
	}
	var segs []string
	if p != "" {
		segs = strings.Split(p, "/")
	}
	bound := make([][]string, len(hr.vars))
	i := 0
	for _, tk := range hr.tokens {
		switch tk.kind {
		case tokLiteral:
			if i >= len(segs) || segs[i] != tk.literal {
				return nil, false
			}
			i++
		case tokStar:
			if i >= len(segs) || segs[i] == "" {
				return nil, false
			}
			if tk.variable >= 0 {
				bound[tk.variable] = append(bound[tk.variable], segs[i])
			}
			i++
		case tokDStar:
			if tk.variable >= 0 {
				bound[tk.variable] = append(bound[tk.variable], segs[i:]...)
			}
			i = len(segs)
		}
		if tk.kind == tokLiteral && tk.variable >= 0 {
			bound[tk.variable] = append(bound[tk.variable], tk.literal)
		}
	}
	if i != len(segs) {
		return nil, false
	}
	vars := make(map[string]string, len(hr.vars))
	for v, field := range hr.vars {
		val, err := url.PathUnescape(strings.Join(bound[v], "/"))
		if err != nil {
			return nil, false
		}
		vars[field] = val
	}
	return vars, true
}

// matchHTTP picks the most specific rule for a request (SPEC 2.2).
func (rs *ruleSet) matchHTTP(method, rawPath string) (*httpRule, map[string]string) {
	for _, hr := range rs.httpRules {
		if hr.method != method {
			continue
		}
		if vars, ok := hr.match(rawPath); ok {
			return hr, vars
		}
	}
	return nil, nil
}

func errTranscode(detail string) error {
	return gatewayError(codes.InvalidArgument, http.StatusBadRequest, detail)
}

// build makes the JSON request message from the body, the path
// variables and the query (SPEC 2.2).
func (hr *httpRule) build(body []byte, vars map[string]string, query url.Values) ([]byte, error) {
	msg := map[string]any{}
	trimmed := bytes.TrimSpace(body)
	switch hr.body {
	case "*":
		if len(trimmed) > 0 {
			v, err := decodeJSON(trimmed)
			if err != nil {
				return nil, err
			}
			obj, ok := v.(map[string]any)
			if !ok {
				return nil, errTranscode("request body must be a JSON object")
			}
			msg = obj
		}
	case "":
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("{}")) {
			return nil, errTranscode("this endpoint takes no request body")
		}
	default:
		if len(trimmed) > 0 {
			v, err := decodeJSON(trimmed)
			if err != nil {
				return nil, err
			}
			if err := setPath(msg, hr.body, v); err != nil {
				return nil, err
			}
		}
	}

	for field, raw := range vars {
		v, err := hr.convert(field, []string{raw})
		if err != nil {
			return nil, err
		}
		if err := setPath(msg, field, v); err != nil {
			return nil, err
		}
	}

	if hr.body != "*" {
		for key, vals := range query {
			if _, byPath := vars[key]; byPath {
				continue
			}
			if len(hr.params) > 0 {
				if _, listed := hr.params[key]; !listed {
					continue
				}
			}
			v, err := hr.convert(key, vals)
			if err != nil {
				return nil, err
			}
			if err := setPath(msg, key, v); err != nil {
				return nil, err
			}
		}
	}
	return json.Marshal(msg)
}

// convert types path and query values: bool and repeated need it, every
// other proto3 JSON scalar accepts a string.
func (hr *httpRule) convert(field string, vals []string) (any, error) {
	typ := hr.params[field]
	one := func(s string) (any, error) {
		if typ != "bool" {
			return s, nil
		}
		switch s {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, errTranscode(fmt.Sprintf("parameter %s must be true or false, got %q", field, s))
	}
	if typ == "repeated" || len(vals) > 1 {
		out := make([]any, 0, len(vals))
		for _, s := range vals {
			v, err := one(s)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return one(vals[0])
}

func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // keep int64 values exact
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, errTranscode("request body is not valid JSON: " + err.Error())
	}
	return v, nil
}

// setPath sets a dotted field path in a JSON object.
func setPath(obj map[string]any, path string, v any) error {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := obj[p]
		if !ok {
			m := map[string]any{}
			obj[p] = m
			obj = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return errTranscode("field " + path + " conflicts with a non-object value at " + p)
		}
		obj = m
	}
	obj[parts[len(parts)-1]] = v
	return nil
}

// reply applies response_body to the JSON response message.
func (hr *httpRule) reply(msg []byte) ([]byte, error) {
	if hr.responseBody == "" {
		return msg, nil
	}
	v, err := decodeJSON(msg)
	if err != nil {
		return nil, errInternal("response is not a JSON object")
	}
	for _, p := range strings.Split(hr.responseBody, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			return []byte("null"), nil
		}
		if v, ok = obj[p]; !ok {
			return []byte("null"), nil // unset fields are omitted by protojson
		}
	}
	return json.Marshal(v)
}
