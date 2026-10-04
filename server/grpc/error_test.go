package grpc

import (
	"testing"

	"github.com/flylib/go-micro/errors"
	"google.golang.org/grpc/codes"
)

func TestMicroErrorGatewayCodes(t *testing.T) {
	tests := []struct {
		code int32
		want codes.Code
	}{
		{429, codes.ResourceExhausted},
		{502, codes.Unavailable},
		{503, codes.Unavailable},
		{504, codes.DeadlineExceeded},
		{400, codes.InvalidArgument},
		{418, codes.Unknown},
	}
	for _, tt := range tests {
		if got := microError(&errors.Error{Code: tt.code}); got != tt.want {
			t.Errorf("microError(%d) = %s, want %s", tt.code, got, tt.want)
		}
	}
}
