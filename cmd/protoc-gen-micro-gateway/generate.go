package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

// maxDepth bounds how far params follows nested messages.
const maxDepth = 4

type rule struct {
	method, path, target, body, responseBody string
	params                                   map[string]string
}

func generate(req *pluginpb.CodeGeneratorRequest) *pluginpb.CodeGeneratorResponse {
	resp := &pluginpb.CodeGeneratorResponse{
		SupportedFeatures: proto.Uint64(uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)),
	}
	fail := func(err error) *pluginpb.CodeGeneratorResponse {
		resp.Error = proto.String(err.Error())
		resp.File = nil
		return resp
	}
	files, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: req.GetProtoFile()})
	if err != nil {
		return fail(err)
	}
	for _, name := range req.GetFileToGenerate() {
		fd, err := files.FindFileByPath(name)
		if err != nil {
			return fail(err)
		}
		var rules []rule
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			ms := svcs.Get(i).Methods()
			for j := 0; j < ms.Len(); j++ {
				rs, err := methodRules(ms.Get(j))
				if err != nil {
					return fail(err)
				}
				rules = append(rules, rs...)
			}
		}
		if len(rules) == 0 {
			continue
		}
		resp.File = append(resp.File, &pluginpb.CodeGeneratorResponse_File{
			Name:    proto.String(strings.TrimSuffix(name, ".proto") + ".gateway.yaml"),
			Content: proto.String(render(name, rules)),
		})
	}
	return resp
}

// methodRules returns the rules of one method's google.api.http option.
func methodRules(m protoreflect.MethodDescriptor) ([]rule, error) {
	opts, ok := m.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, annotations.E_Http) {
		return nil, nil
	}
	hr, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule)
	if !ok || hr == nil {
		return nil, nil
	}
	if m.IsStreamingClient() || m.IsStreamingServer() {
		return nil, fmt.Errorf("%s: google.api.http on a streaming method; the gateway's HTTP entry is unary only", m.FullName())
	}
	target := fmt.Sprintf("/%s/%s", m.Parent().FullName(), m.Name())
	params := map[string]string{}
	collectParams(m.Input(), "", params, 0, map[protoreflect.FullName]bool{})

	var out []rule
	for _, b := range append([]*annotations.HttpRule{hr}, hr.GetAdditionalBindings()...) {
		method, path, err := pattern(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", m.FullName(), err)
		}
		out = append(out, rule{
			method: method, path: path, target: target,
			body: b.GetBody(), responseBody: b.GetResponseBody(), params: params,
		})
	}
	return out, nil
}

func pattern(b *annotations.HttpRule) (string, string, error) {
	switch p := b.GetPattern().(type) {
	case *annotations.HttpRule_Get:
		return "GET", p.Get, nil
	case *annotations.HttpRule_Put:
		return "PUT", p.Put, nil
	case *annotations.HttpRule_Post:
		return "POST", p.Post, nil
	case *annotations.HttpRule_Delete:
		return "DELETE", p.Delete, nil
	case *annotations.HttpRule_Patch:
		return "PATCH", p.Patch, nil
	case *annotations.HttpRule_Custom:
		return "", "", fmt.Errorf("custom method %q is not supported by the gateway", p.Custom.GetKind())
	}
	return "", "", fmt.Errorf("google.api.http without a pattern")
}

// collectParams lists the fields of msg by proto field path with the type
// the gateway converts to (SPEC 2.2): bool, repeated, number or string.
func collectParams(msg protoreflect.MessageDescriptor, prefix string, out map[string]string, depth int, seen map[protoreflect.FullName]bool) {
	if seen[msg.FullName()] {
		return // recursive message
	}
	seen[msg.FullName()] = true
	defer delete(seen, msg.FullName())

	fields := msg.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		name := prefix + string(f.Name())
		switch {
		case f.IsMap():
			continue // not settable from paths or queries
		case f.IsList():
			out[name] = "repeated"
		case f.Kind() == protoreflect.BoolKind:
			out[name] = "bool"
		case f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind:
			if typ, ok := wellKnown(f.Message()); ok {
				out[name] = typ
			} else if depth+1 < maxDepth {
				collectParams(f.Message(), name+".", out, depth+1, seen)
			}
		case isNumber(f.Kind()):
			out[name] = "number"
		default: // string, bytes, enum
			out[name] = "string"
		}
	}
}

// wellKnown types are JSON scalars in proto3 JSON.
func wellKnown(m protoreflect.MessageDescriptor) (string, bool) {
	switch m.FullName() {
	case "google.protobuf.BoolValue":
		return "bool", true
	case "google.protobuf.Int32Value", "google.protobuf.Int64Value", "google.protobuf.UInt32Value",
		"google.protobuf.UInt64Value", "google.protobuf.FloatValue", "google.protobuf.DoubleValue":
		return "number", true
	case "google.protobuf.StringValue", "google.protobuf.BytesValue", "google.protobuf.Timestamp",
		"google.protobuf.Duration", "google.protobuf.FieldMask":
		return "string", true
	}
	return "", false
}

func isNumber(k protoreflect.Kind) bool {
	switch k {
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed64Kind, protoreflect.FloatKind, protoreflect.DoubleKind:
		return true
	}
	return false
}

// q renders a YAML scalar: a JSON string is valid YAML and needs no
// further escaping for the braces, colons and stars of templates.
func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func render(source string, rules []rule) string {
	var b strings.Builder
	p := func(parts ...string) {
		for _, s := range parts {
			b.WriteString(s)
		}
		b.WriteByte('\n')
	}
	p("# Code generated by protoc-gen-micro-gateway. DO NOT EDIT.")
	p("# source: ", source)
	p("#")
	p("# Gateway HTTP rules (gateway/SPEC.md section 2.2): merge this list into")
	p("# the http_rules of the gateway rules document.")
	p("http_rules:")
	for _, r := range rules {
		p("  - method: ", r.method)
		p("    path: ", q(r.path))
		p("    target: ", q(r.target))
		if r.body != "" {
			p("    body: ", q(r.body))
		}
		if r.responseBody != "" {
			p("    response_body: ", q(r.responseBody))
		}
		if len(r.params) > 0 {
			keys := make([]string, 0, len(r.params))
			for k := range r.params {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, len(keys))
			for i, k := range keys {
				parts[i] = q(k) + ": " + r.params[k]
			}
			p("    params: {", strings.Join(parts, ", "), "}")
		}
	}
	return b.String()
}
