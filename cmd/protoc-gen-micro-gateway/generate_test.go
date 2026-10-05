package main

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"google.golang.org/protobuf/types/pluginpb"
)

func field(name string, num int32, typ descriptorpb.FieldDescriptorProto_Type, label descriptorpb.FieldDescriptorProto_Label, typeName string) *descriptorpb.FieldDescriptorProto {
	f := &descriptorpb.FieldDescriptorProto{
		Name: proto.String(name), Number: proto.Int32(num), Type: typ.Enum(), Label: label.Enum(),
		JsonName: proto.String(name),
	}
	if typeName != "" {
		f.TypeName = proto.String(typeName)
	}
	return f
}

func method(name, in string, rule *annotations.HttpRule, stream bool) *descriptorpb.MethodDescriptorProto {
	m := &descriptorpb.MethodDescriptorProto{
		Name: proto.String(name), InputType: proto.String(in), OutputType: proto.String(".users.User"),
	}
	if stream {
		m.ServerStreaming = proto.Bool(true)
	}
	if rule != nil {
		m.Options = &descriptorpb.MethodOptions{}
		proto.SetExtension(m.Options, annotations.E_Http, rule)
	}
	return m
}

const (
	opt = descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	rep = descriptorpb.FieldDescriptorProto_LABEL_REPEATED
)

func run(t *testing.T, methods ...*descriptorpb.MethodDescriptorProto) (string, error) {
	t.Helper()
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE
	file := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("users/users.proto"),
		Package:    proto.String("users"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/api/annotations.proto", "google/protobuf/wrappers.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Page"), Field: []*descriptorpb.FieldDescriptorProto{
				field("size", 1, descriptorpb.FieldDescriptorProto_TYPE_INT32, opt, ""),
				field("token", 2, str, opt, ""),
				field("next", 3, msg, opt, ".users.Page"), // recursive
			}},
			{Name: proto.String("ListRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				field("org", 1, str, opt, ""),
				field("verbose", 2, descriptorpb.FieldDescriptorProto_TYPE_BOOL, opt, ""),
				field("tags", 3, str, rep, ""),
				field("page", 4, msg, opt, ".users.Page"),
				field("active", 5, msg, opt, ".google.protobuf.BoolValue"),
				field("score", 6, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, opt, ""),
			}},
			{Name: proto.String("User"), Field: []*descriptorpb.FieldDescriptorProto{
				field("id", 1, str, opt, ""),
				field("name", 2, str, opt, ""),
			}},
			{Name: proto.String("UpdateRequest"), Field: []*descriptorpb.FieldDescriptorProto{
				field("id", 1, str, opt, ""),
				field("user", 2, msg, opt, ".users.User"),
			}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Users"), Method: methods}},
	}
	req := &pluginpb.CodeGeneratorRequest{
		FileToGenerate: []string{"users/users.proto"},
		ProtoFile: []*descriptorpb.FileDescriptorProto{
			protodesc.ToFileDescriptorProto(descriptorpb.File_google_protobuf_descriptor_proto),
			protodesc.ToFileDescriptorProto(annotations.File_google_api_http_proto),
			protodesc.ToFileDescriptorProto(annotations.File_google_api_annotations_proto),
			protodesc.ToFileDescriptorProto(wrapperspb.File_google_protobuf_wrappers_proto),
			file,
		},
	}
	resp := generate(req)
	if resp.Error != nil {
		return "", errors.New(resp.GetError())
	}
	for _, f := range resp.File {
		if f.GetName() == "users/users.gateway.yaml" {
			return f.GetContent(), nil
		}
	}
	return "", nil
}

func TestGenerate(t *testing.T) {
	out, err := run(t,
		method("List", ".users.ListRequest", &annotations.HttpRule{
			Pattern: &annotations.HttpRule_Get{Get: "/v1/orgs/{org}/users"},
			AdditionalBindings: []*annotations.HttpRule{
				{Pattern: &annotations.HttpRule_Post{Post: "/v1/orgs/{org}/users:search"}, Body: "*"},
			},
		}, false),
		method("Update", ".users.UpdateRequest", &annotations.HttpRule{
			Pattern: &annotations.HttpRule_Patch{Patch: "/v1/users/{id}"}, Body: "user", ResponseBody: "name",
		}, false),
		method("Plain", ".users.User", nil, false),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `http_rules:
  - method: GET
    path: "/v1/orgs/{org}/users"
    target: "/users.Users/List"
    params: {"active": bool, "org": string, "page.size": number, "page.token": string, "score": number, "tags": repeated, "verbose": bool}
  - method: POST
    path: "/v1/orgs/{org}/users:search"
    target: "/users.Users/List"
    body: "*"
    params: {"active": bool, "org": string, "page.size": number, "page.token": string, "score": number, "tags": repeated, "verbose": bool}
  - method: PATCH
    path: "/v1/users/{id}"
    target: "/users.Users/Update"
    body: "user"
    response_body: "name"
    params: {"id": string, "user.id": string, "user.name": string}
`
	if i := strings.Index(out, "http_rules:"); i < 0 || out[i:] != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}
}

func TestGenerateRefuses(t *testing.T) {
	get := &annotations.HttpRule{Pattern: &annotations.HttpRule_Get{Get: "/v1/u"}}
	if _, err := run(t, method("Watch", ".users.User", get, true)); err == nil || !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("streaming method: %v", err)
	}
	custom := &annotations.HttpRule{Pattern: &annotations.HttpRule_Custom{Custom: &annotations.CustomHttpPattern{Kind: "HEAD", Path: "/v1/u"}}}
	if _, err := run(t, method("Head", ".users.User", custom, false)); err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("custom pattern: %v", err)
	}
	if out, err := run(t, method("Plain", ".users.User", nil, false)); err != nil || out != "" {
		t.Fatalf("no annotations: %q %v", out, err)
	}
}
