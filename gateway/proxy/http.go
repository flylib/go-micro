package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	merr "github.com/flylib/go-micro/errors"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// The HTTP/JSON entry (SPEC 2.1): POST /api/<service>/<Handler>/<Method>
// with a JSON body becomes the gRPC call /<service>.<Handler>/<Method>,
// sent as one application/grpc+json message. After the path is mapped,
// the call takes the same route, plugins, discovery and retries as one on
// the gRPC entry.

// maxHTTPBody is the request size limit, gRPC's default message size.
const maxHTTPBody = 4 << 20

// httpOnlyHeaders are HTTP headers that are not call metadata.
var httpOnlyHeaders = map[string]bool{
	"connection":        true,
	"keep-alive":        true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
	"host":              true,
	"content-length":    true,
	"content-type":      true,
	"accept-encoding":   true,
}

// HTTPHandler returns the HTTP/JSON entry as an http.Handler, for mounting
// in an existing server. ServeAPI serves it with h2c as well.
func (g *Gateway) HTTPHandler() http.Handler {
	r := chi.NewRouter()
	r.Post("/api/{service}/{handler}/{method}", g.serveJSON)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeHTTPError(w, errHTTPNotFound(r.URL.Path), "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		writeHTTPError(w, errHTTPMethod(r.Method), "")
	})
	return r
}

// ServeAPI serves the HTTP/JSON entry on l, HTTP/1.1 and h2c, until Stop.
func (g *Gateway) ServeAPI(l net.Listener) error {
	srv := &http.Server{
		Handler:           h2c.NewHandler(g.HTTPHandler(), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	g.httpMu.Lock()
	g.httpSrvs = append(g.httpSrvs, srv)
	g.httpMu.Unlock()
	if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (g *Gateway) serveJSON(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	method := jsonMethod(chi.URLParam(r, "service"), chi.URLParam(r, "handler"), chi.URLParam(r, "method"))
	md := httpMetadata(r.Header)
	c := &call{
		ctx:      r.Context(),
		entry:    "http",
		method:   method,
		md:       md,
		clientIP: clientIP(hostOfString(r.RemoteAddr), md, g.opts.TrustedProxies),
	}

	var pc *prepared
	var node string
	var err error
	defer func() { g.access(c, pc, node, start, err) }()

	if err = checkContentType(r.Header.Get("Content-Type")); err != nil {
		writeHTTPError(w, err, "")
		return
	}
	body, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxHTTPBody))
	if rerr != nil {
		var tooBig *http.MaxBytesError
		if errors.As(rerr, &tooBig) {
			err = errHTTPTooLarge()
		} else {
			err = errInternal("read request body: " + rerr.Error())
		}
		writeHTTPError(w, err, "")
		return
	}

	var reply *frame
	var hdr metadata.MD
	pc, reply, hdr, err = g.callJSON(r.Context(), c, body, &node)
	if err != nil {
		writeHTTPError(w, err, serviceOf(pc))
		return
	}
	for k, vs := range hdr {
		if replyHeader(k) {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(reply.data)
}

// callJSON runs one HTTP/JSON call once the entry has checked and read the
// request, whichever HTTP server it runs on: prepare, open a node, send
// the body as one json message and take the one reply (SPEC 2.1).
func (g *Gateway) callJSON(ctx context.Context, c *call, body []byte, node *string) (*prepared, *frame, metadata.MD, error) {
	if len(body) == 0 {
		body = []byte("{}")
	}
	pc, err := g.prepare(c)
	if err != nil {
		return pc, nil, nil, err
	}
	up, cancel, done, err := g.open(ctx, pc, "json", node)
	if err != nil {
		return pc, nil, nil, err
	}
	defer cancel()
	defer done()
	reply, hdr, err := unary(up, cancel, pc.route, &frame{data: body})
	if errors.Is(err, errUpstreamTimedOut) {
		err = errUpstreamTimeout(pc.service)
	}
	return pc, reply, hdr, err
}

// jsonMethod is the gRPC path of /api/<service>/<Handler>/<Method>.
func jsonMethod(service, handler, method string) string {
	return "/" + service + "." + handler + "/" + method
}

// checkContentType accepts no content type or application/json.
func checkContentType(ct string) error {
	if ct == "" {
		return nil
	}
	if mt, _, err := mime.ParseMediaType(ct); err != nil || mt != "application/json" {
		return errHTTPMediaType(ct)
	}
	return nil
}

// replyHeader reports whether reply metadata key k becomes a header.
func replyHeader(k string) bool {
	return k != "content-type" && !strings.HasPrefix(k, "grpc-")
}

func serviceOf(pc *prepared) string {
	if pc == nil {
		return ""
	}
	return pc.service
}

var errUpstreamTimedOut = errors.New("upstream timed out")

// unary sends one request frame and expects exactly one reply frame,
// with the route's send and read timeouts (SPEC 6).
func unary(up grpc.ClientStream, cancel context.CancelFunc, rt *route, req *frame) (*frame, metadata.MD, error) {
	var timedOut atomic.Bool
	expire := func() { timedOut.Store(true); cancel() }
	wrap := func(err error) error {
		if timedOut.Load() {
			return errUpstreamTimedOut
		}
		return err
	}

	t := time.AfterFunc(rt.send, expire)
	serr := up.SendMsg(req)
	t.Stop()
	if serr == nil {
		_ = up.CloseSend()
	}
	// a failed send means the upstream ended: its status comes from RecvMsg

	reply := &frame{}
	t = time.AfterFunc(rt.read, expire)
	err := up.RecvMsg(reply)
	t.Stop()
	if errors.Is(err, io.EOF) {
		return nil, nil, errInternal("upstream sent no reply")
	}
	if err != nil {
		return nil, nil, wrap(err)
	}
	hdr, _ := up.Header()

	var extra frame
	t = time.AfterFunc(rt.read, expire)
	err = up.RecvMsg(&extra)
	t.Stop()
	switch {
	case err == nil:
		return nil, nil, errInternal("streaming methods are not supported over HTTP")
	case !errors.Is(err, io.EOF):
		return nil, nil, wrap(err)
	}
	return reply, hdr, nil
}

// httpMetadata turns request headers into call metadata (SPEC 2.1).
func httpMetadata(h http.Header) metadata.MD {
	md := metadata.MD{}
	for k, vs := range h {
		lk := strings.ToLower(k)
		if httpOnlyHeaders[lk] || strings.HasPrefix(lk, "proxy-") || !validMetadataKey(lk) {
			continue
		}
		md[lk] = append(md[lk], vs...)
	}
	return md
}

// validMetadataKey is gRPC's rule for metadata keys; other HTTP header
// names would make the upstream call fail.
func validMetadataKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func hostOfString(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// writeHTTPError renders err as a go-micro JSON error (SPEC 2.1). A
// go-micro error, from a service or the gateway, is written as is with
// its code as the status; any other gRPC error maps by its code.
func writeHTTPError(w http.ResponseWriter, err error, service string) {
	code, body := httpError(err, service)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

func httpError(err error, service string) (int, []byte) {
	st := status.Convert(err)
	var me merr.Error
	if json.Unmarshal([]byte(st.Message()), &me) == nil && me.Code >= 100 && me.Code <= 599 {
		return int(me.Code), []byte(st.Message())
	}
	code := httpStatusFromGRPC(st.Code())
	id := service
	if id == "" {
		id = errorID
	}
	return code, []byte(merr.New(id, st.Message(), int32(code)).Error())
}

// httpStatusFromGRPC is the mapping go-micro's gRPC client uses
// (client/grpc microStatusFromGrpcCode).
func httpStatusFromGRPC(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.NotFound:
		return http.StatusNotFound
	case codes.DeadlineExceeded:
		return http.StatusRequestTimeout
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.FailedPrecondition:
		return http.StatusPreconditionFailed
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

// Requests the HTTP entry refuses itself (SPEC 2.1).

func errHTTPNotFound(path string) error {
	return gatewayError(codes.NotFound, http.StatusNotFound, "no API endpoint at "+path+": want /api/<service>/<Handler>/<Method>")
}

func errHTTPMethod(m string) error {
	return gatewayError(codes.Unimplemented, http.StatusMethodNotAllowed, "method "+m+" not allowed: use POST")
}

func errHTTPMediaType(ct string) error {
	return gatewayError(codes.InvalidArgument, http.StatusUnsupportedMediaType, "content type "+ct+" not supported: use application/json")
}

func errHTTPTooLarge() error {
	return gatewayError(codes.ResourceExhausted, http.StatusRequestEntityTooLarge, "request body larger than 4 MiB")
}
