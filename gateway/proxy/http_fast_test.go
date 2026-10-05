package proxy

import (
	"encoding/json"
	"net"
	"testing"

	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/registry"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttputil"
)

// TestFastRefusals runs the net/http entry's refusal cases against the
// fasthttp entry: both must answer alike (SPEC 2.1).
func TestFastRefusals(t *testing.T) {
	gw, err := New(Registry(registry.NewMemoryRegistry()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Stop)
	ln := fasthttputil.NewInmemoryListener()
	go func() { _ = gw.ServeAPIFast(ln) }()
	c := &fasthttp.Client{Dial: func(string) (net.Conn, error) { return ln.Dial() }}

	cases := []struct {
		name, method, path, ctype, body string
		want                            int
	}{
		{"too few segments", "POST", "/api/svc/Handler", "", "", 404},
		{"not under /api", "POST", "/svc/Handler/Method", "", "", 404},
		{"GET", "GET", "/api/svc/Handler/Method", "", "", 405},
		{"text body", "POST", "/api/svc/Handler/Method", "text/plain", "x", 415},
		{"no nodes", "POST", "/api/svc/Handler/Method", "application/json; charset=utf-8", "{}", 503},
	}
	for _, tc := range cases {
		req, rsp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
		req.SetRequestURI("http://gw" + tc.path)
		req.Header.SetMethod(tc.method)
		if tc.ctype != "" {
			req.Header.SetContentType(tc.ctype)
		}
		req.SetBodyString(tc.body)
		if err := c.Do(req, rsp); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		e := &merr.Error{}
		if err := json.Unmarshal(rsp.Body(), e); err != nil {
			t.Errorf("%s: body is not a go-micro error: %q", tc.name, rsp.Body())
		}
		if rsp.StatusCode() != tc.want || e.Code != int32(tc.want) || e.Id != errorID {
			t.Errorf("%s: status %d body %s, want %d from %s", tc.name, rsp.StatusCode(), rsp.Body(), tc.want, errorID)
		}
		if ct := string(rsp.Header.ContentType()); ct != "application/json" {
			t.Errorf("%s: content type %q", tc.name, ct)
		}
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(rsp)
	}
}

// TestFastBodyTooLarge checks the answer to bodies fasthttp rejects by
// Content-Length before the handler runs; fasthttp then closes the
// connection, so a client still writing the body may see only that.
func TestFastBodyTooLarge(t *testing.T) {
	var ctx fasthttp.RequestCtx
	fastParseError(&ctx, fasthttp.ErrBodyTooLarge)
	e := &merr.Error{}
	if err := json.Unmarshal(ctx.Response.Body(), e); err != nil || ctx.Response.StatusCode() != 413 || e.Code != 413 || e.Id != errorID {
		t.Fatalf("status %d body %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

func TestSplitAPIPath(t *testing.T) {
	if s, h, m, ok := splitAPIPath("/api/greeter/Greeter/Hello"); !ok || s != "greeter" || h != "Greeter" || m != "Hello" {
		t.Errorf("got %q %q %q %v", s, h, m, ok)
	}
	for _, bad := range []string{"/api/a/b", "/api/a/b/c/d", "/api//b/c", "/x/a/b/c", "/api/a/b/"} {
		if _, _, _, ok := splitAPIPath(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}
