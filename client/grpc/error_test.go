package grpc

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

func TestMicroStatusFromGrpcCodeResourceExhausted(t *testing.T) {
	if got := microStatusFromGrpcCode(codes.ResourceExhausted); got != http.StatusTooManyRequests {
		t.Errorf("ResourceExhausted -> %d, want 429", got)
	}
}
