package proxy

import (
	"net/http"

	merr "github.com/flylib/go-micro/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errorID is the go-micro error id of every gateway-originated error, so
// clients can tell them from service errors (SPEC 8).
const errorID = "micro.gateway"

// gatewayError builds a gateway-originated error: a gRPC status whose
// message is a go-micro error, the shape services produce (SPEC 8).
func gatewayError(code codes.Code, httpCode int32, detail string) error {
	return status.Error(code, merr.New(errorID, detail, httpCode).Error())
}

// errMalformedPath matches what grpc-go answers for paths it rejects
// before routing, so every malformed path looks the same (SPEC 3.1).
func errMalformedPath(path string) error {
	return gatewayError(codes.Unimplemented, http.StatusNotImplemented, "malformed method path "+path)
}

func errNoRoute(path string) error {
	return gatewayError(codes.Unimplemented, http.StatusNotImplemented, "no route for "+path)
}

func errUnauthenticated(detail string) error {
	return gatewayError(codes.Unauthenticated, http.StatusUnauthorized, detail)
}

func errForbidden(detail string) error {
	return gatewayError(codes.PermissionDenied, http.StatusForbidden, detail)
}

func errRateLimited() error {
	return gatewayError(codes.ResourceExhausted, http.StatusTooManyRequests, "rate limit exceeded")
}

func errNoNodes(service string) error {
	return gatewayError(codes.Unavailable, http.StatusServiceUnavailable, "no available nodes for "+service)
}

func errUpstreamConnect(service string, err error) error {
	return gatewayError(codes.Unavailable, http.StatusBadGateway, "could not reach "+service+": "+err.Error())
}

func errUpstreamTimeout(service string) error {
	return gatewayError(codes.DeadlineExceeded, http.StatusGatewayTimeout, "upstream "+service+" timed out")
}

func gatewayErrorBadRequest(detail string) error {
	return gatewayError(codes.InvalidArgument, http.StatusBadRequest, detail)
}

func errInternal(detail string) error {
	return gatewayError(codes.Internal, http.StatusInternalServerError, detail)
}
