// protoc-gen-micro-gateway generates gateway HTTP rules (gateway/SPEC.md
// section 2.2) from google.api.http annotations: for each .proto file with
// annotated methods, a <file>.gateway.yaml next to it (relative to the
// output directory) holding an http_rules list to merge into the gateway
// rules document.
//
//	protoc -I. -I<googleapis> --micro-gateway_out=. greeter.proto
//
// Each annotated method gives one rule, plus one per additional_binding.
// The target is the method's gRPC path, /<package>.<Service>/<Method>,
// which is the go-micro call path when the proto package is the service's
// registry name. params lists the request message's fields with the type
// the gateway needs to know (bool, repeated), so query parameters that are
// not fields of the request are ignored.
package main

import (
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

func main() {
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "protoc-gen-micro-gateway is a protoc plugin: protoc --micro-gateway_out=. x.proto")
		os.Exit(2)
	}
	in, err := io.ReadAll(os.Stdin)
	if err != nil {
		fatal(err)
	}
	req := &pluginpb.CodeGeneratorRequest{}
	if err := proto.Unmarshal(in, req); err != nil {
		fatal(err)
	}
	out, err := proto.Marshal(generate(req))
	if err != nil {
		fatal(err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "protoc-gen-micro-gateway:", err)
	os.Exit(1)
}
