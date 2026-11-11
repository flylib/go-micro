package grpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"github.com/flylib/go-micro/errors"
)

// connectErrorID matches client.ConnectErrorID: no connection could be
// made, so the request was not sent.
const connectErrorID = "go.micro.client.connect"

const callerID = "go.micro.client"

// h2c speaks cleartext HTTP/2, which gRPC servers accept.
var h2c = &http.Client{Transport: &http2.Transport{
	AllowHTTP: true,
	DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
	},
}}

// Path returns the gRPC path of a go-micro endpoint: "Handler.Method" of
// service becomes "/service.Handler/Method". A path that already starts
// with "/" is returned as is.
func Path(service, endpoint string) string {
	if strings.HasPrefix(endpoint, "/") {
		return endpoint
	}
	handler, method, ok := strings.Cut(endpoint, ".")
	if !ok {
		return "/" + service + "/" + endpoint
	}
	return "/" + service + "." + handler + "/" + method
}

// CallJSON makes one unary gRPC call to a node with a JSON request and
// returns the JSON reply. It sends the message as application/grpc+json,
// which go-micro's gRPC server decodes, so no generated code and no gRPC
// library is needed: it is what the CLI and the dashboard use to call
// services on the gRPC server. md is sent as request metadata.
//
// A failure is a go-micro error: the service's own, a connect error (id
// go.micro.client.connect) when the node cannot be reached, or one made
// from the gRPC status.
func CallJSON(ctx context.Context, address, path string, body []byte, md map[string]string) ([]byte, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	frame := make([]byte, 5+len(body))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(body)))
	copy(frame[5:], body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+path, bytes.NewReader(frame))
	if err != nil {
		return nil, errors.BadRequest(callerID, "%v", err)
	}
	for k, v := range md {
		k = strings.ToLower(k)
		switch k {
		case "content-type", "te", "connection", "content-length", "host", "user-agent", "accept-encoding":
			continue
		}
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/grpc+json")
	req.Header.Set("TE", "trailers")
	if d, ok := ctx.Deadline(); ok {
		if ms := time.Until(d).Milliseconds(); ms > 0 {
			req.Header.Set("Grpc-Timeout", strconv.FormatInt(ms, 10)+"m")
		}
	}

	rsp, err := h2c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Timeout(callerID, "%v", ctx.Err())
		}
		// nothing was answered: the node is down, or it does not speak HTTP/2
		return nil, errors.New(connectErrorID, fmt.Sprintf("connect %s: %v", address, err), http.StatusServiceUnavailable)
	}
	defer rsp.Body.Close()
	data, rerr := io.ReadAll(rsp.Body) // trailers arrive once the body is read

	status := rsp.Trailer.Get("Grpc-Status")
	message := rsp.Trailer.Get("Grpc-Message")
	if status == "" { // a trailers-only reply carries them as headers
		status, message = rsp.Header.Get("Grpc-Status"), rsp.Header.Get("Grpc-Message")
	}
	switch {
	case status == "" && rerr != nil:
		if ctx.Err() != nil {
			return nil, errors.Timeout(callerID, "%v", ctx.Err())
		}
		return nil, errors.InternalServerError(callerID, "read reply from %s: %v", address, rerr)
	case status == "":
		return nil, errors.InternalServerError(callerID, "%s answered HTTP %d without a gRPC status: not a gRPC server?", address, rsp.StatusCode)
	case status != "0":
		return nil, statusError(status, message)
	}
	if len(data) < 5 {
		return nil, errors.InternalServerError(callerID, "%s sent no reply message", address)
	}
	if data[0] != 0 {
		return nil, errors.InternalServerError(callerID, "%s sent a compressed reply", address)
	}
	n := int(binary.BigEndian.Uint32(data[1:5]))
	if n > len(data)-5 {
		return nil, errors.InternalServerError(callerID, "%s sent a truncated reply", address)
	}
	return data[5 : 5+n], nil
}

// statusError turns a non-OK gRPC status into a go-micro error: the
// service's own error when the message carries one.
func statusError(status, message string) error {
	if m, err := url.PathUnescape(message); err == nil {
		message = m // grpc-message is percent-encoded
	}
	if e := errors.Parse(message); e.Code > 0 {
		return e
	}
	code, _ := strconv.Atoi(status)
	return errors.New(callerID, message, httpStatus(code))
}

// httpStatus maps a gRPC status code as go-micro's gRPC client does.
func httpStatus(code int) int32 {
	switch code {
	case 3: // InvalidArgument
		return http.StatusBadRequest
	case 4: // DeadlineExceeded
		return http.StatusRequestTimeout
	case 5: // NotFound
		return http.StatusNotFound
	case 6: // AlreadyExists
		return http.StatusConflict
	case 7: // PermissionDenied
		return http.StatusForbidden
	case 8: // ResourceExhausted
		return http.StatusTooManyRequests
	case 9: // FailedPrecondition
		return http.StatusPreconditionFailed
	case 12: // Unimplemented
		return http.StatusNotImplemented
	case 14: // Unavailable
		return http.StatusServiceUnavailable
	case 16: // Unauthenticated
		return http.StatusUnauthorized
	}
	return http.StatusInternalServerError
}
