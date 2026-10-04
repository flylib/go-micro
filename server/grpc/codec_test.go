package grpc

import (
	"testing"
)

// legacyMessage is a v1-only protobuf message, the shape the etcd v3.5
// client sends through the global "proto" codec this package installs.
type legacyMessage struct {
	Key string `protobuf:"bytes,1,opt,name=key,proto3"`
}

func (m *legacyMessage) Reset()         { *m = legacyMessage{} }
func (m *legacyMessage) String() string { return m.Key }
func (*legacyMessage) ProtoMessage()    {}

func TestProtoCodecLegacyMessage(t *testing.T) {
	b, err := protoCodec{}.Marshal(&legacyMessage{Key: "/micro/registry/x"})
	if err != nil {
		t.Fatalf("marshal legacy message: %v", err)
	}
	var got legacyMessage
	if err := (protoCodec{}).Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal legacy message: %v", err)
	}
	if got.Key != "/micro/registry/x" {
		t.Fatalf("round trip = %q", got.Key)
	}
	if _, err := (protoCodec{}).Marshal(struct{}{}); err == nil {
		t.Fatal("non-message accepted")
	}
}
