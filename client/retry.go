package client

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flylib/go-micro/errors"
)

// note that returning either false or a non-nil error will result in the call not being retried.
type RetryFunc func(ctx context.Context, req Request, retryCount int, err error) (bool, error)

// ConnectErrorID is the id of errors reporting that no connection to the
// selected node could be made, so the request was never sent. Such a
// call is safe to retry on another node whatever it does.
const ConnectErrorID = "go.micro.client.connect"

// ConnectError returns the error of a failed connection to a node.
func ConnectError(address string, err error) error {
	return errors.New(ConnectErrorID, fmt.Sprintf("connect %s: %v", address, err), http.StatusServiceUnavailable)
}

// IsConnectError reports whether err says the request was never sent
// because the node could not be connected to.
func IsConnectError(err error) bool {
	if err == nil {
		return false
	}
	e := errors.Parse(err.Error())
	return e != nil && e.Id == ConnectErrorID
}

// RetryAlways always retry on error.
func RetryAlways(ctx context.Context, req Request, retryCount int, err error) (bool, error) {
	return true, nil
}

// RetryOnConnect retries only calls that never reached a node: the next
// attempt goes to another node. It is the default. A request that was
// sent is not retried, since the service may have acted on it.
func RetryOnConnect(ctx context.Context, req Request, retryCount int, err error) (bool, error) {
	return IsConnectError(err), nil
}

// RetryOnError retries calls that never reached a node and calls that
// timed out (408). A timed out request may have been executed: use it
// only for idempotent endpoints.
func RetryOnError(ctx context.Context, req Request, retryCount int, err error) (bool, error) {
	if err == nil {
		return false, nil
	}

	e := errors.Parse(err.Error())
	if e == nil {
		return false, nil
	}
	if e.Id == ConnectErrorID {
		return true, nil
	}

	switch e.Code {
	// Retry on timeout, not on 500 internal server error, as that is a business
	// logic error that should be handled by the user.
	case 408:
		return true, nil
	default:
		return false, nil
	}
}
