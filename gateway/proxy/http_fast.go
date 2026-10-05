package proxy

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	"google.golang.org/grpc/metadata"
)

// The HTTP/JSON entry on fasthttp: same mapping, checks and call path as
// the net/http one (SPEC 2.1), HTTP/1.1 only. fasthttp has no HTTP/2, so
// this server does not offer h2c.

// ServeAPIFast serves the HTTP/JSON entry on l with fasthttp, until Stop.
func (g *Gateway) ServeAPIFast(l net.Listener) error {
	srv := &fasthttp.Server{
		Handler:               g.serveFast,
		ErrorHandler:          fastParseError,
		MaxRequestBodySize:    maxHTTPBody,
		ReadTimeout:           5 * time.Minute, // whole request; calls themselves have route timeouts
		IdleTimeout:           2 * time.Minute,
		NoDefaultServerHeader: true,
		NoDefaultContentType:  true,
	}
	g.httpMu.Lock()
	g.fastSrvs = append(g.fastSrvs, srv)
	g.httpMu.Unlock()
	return srv.Serve(l)
}

func (g *Gateway) serveFast(ctx *fasthttp.RequestCtx) {
	start := time.Now()
	md := fastMetadata(&ctx.Request.Header)
	c := &call{
		ctx:      context.Background(),
		entry:    "http",
		md:       md,
		clientIP: clientIP(ctx.RemoteIP().String(), md, g.opts.TrustedProxies),
	}

	var pc *prepared
	var node string
	var err error
	defer func() { g.access(c, pc, node, start, err) }()

	// HTTP rules first (SPEC 2.2), on the raw path
	raw, _, _ := strings.Cut(string(ctx.Request.URI().PathOriginal()), "?")
	if hr, vars := g.rules.Load().matchHTTP(string(ctx.Method()), raw); hr != nil {
		c.method = hr.target
		if err = checkContentType(string(ctx.Request.Header.ContentType())); err != nil {
			writeFastError(ctx, err, "")
			return
		}
		query, qerr := url.ParseQuery(string(ctx.QueryArgs().QueryString()))
		if qerr != nil {
			err = errTranscode("bad query string: " + qerr.Error())
			writeFastError(ctx, err, "")
			return
		}
		var out []byte
		var hdr metadata.MD
		pc, out, hdr, err = g.transcode(c.ctx, c, hr, vars, query, ctx.PostBody(), &node)
		if err != nil {
			writeFastError(ctx, err, serviceOf(pc))
			return
		}
		writeFastReply(ctx, hdr, out)
		return
	}

	// route: /api/<service>/<Handler>/<Method>, POST only (as with chi:
	// 404 when the path does not match, 405 when only the method is wrong)
	path := string(ctx.Path())
	c.method = path
	service, handler, method, ok := splitAPIPath(path)
	if !ok {
		err = errHTTPNotFound(path)
		writeFastError(ctx, err, "")
		return
	}
	c.method = jsonMethod(service, handler, method)
	if !ctx.IsPost() {
		ctx.Response.Header.Set("Allow", fasthttp.MethodPost)
		err = errHTTPMethod(string(ctx.Method()))
		writeFastError(ctx, err, "")
		return
	}
	if err = checkContentType(string(ctx.Request.Header.ContentType())); err != nil {
		writeFastError(ctx, err, "")
		return
	}

	var reply *frame
	var hdr metadata.MD
	// the body stays valid until the handler returns, after the call ends
	pc, reply, hdr, err = g.callJSON(c.ctx, c, ctx.PostBody(), &node)
	if err != nil {
		writeFastError(ctx, err, serviceOf(pc))
		return
	}
	writeFastReply(ctx, hdr, reply.data)
}

func writeFastReply(ctx *fasthttp.RequestCtx, hdr metadata.MD, body []byte) {
	for k, vs := range hdr {
		if replyHeader(k) {
			for _, v := range vs {
				ctx.Response.Header.Add(k, v)
			}
		}
	}
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetBody(body)
}

// splitAPIPath splits /api/<service>/<Handler>/<Method>, every part set.
func splitAPIPath(p string) (service, handler, method string, ok bool) {
	rest, found := strings.CutPrefix(p, "/api/")
	if !found {
		return "", "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// fastMetadata turns request headers into call metadata (SPEC 2.1).
func fastMetadata(h *fasthttp.RequestHeader) metadata.MD {
	md := metadata.MD{}
	for k, v := range h.All() {
		lk := strings.ToLower(string(k))
		if httpOnlyHeaders[lk] || strings.HasPrefix(lk, "proxy-") || !validMetadataKey(lk) {
			continue
		}
		md[lk] = append(md[lk], string(v))
	}
	return md
}

func writeFastError(ctx *fasthttp.RequestCtx, err error, service string) {
	code, body := httpError(err, service)
	ctx.SetContentType("application/json")
	ctx.SetStatusCode(code)
	ctx.SetBody(body)
}

// fastParseError answers requests fasthttp rejects before the handler,
// with the same go-micro errors as the net/http entry.
func fastParseError(ctx *fasthttp.RequestCtx, err error) {
	if errors.Is(err, fasthttp.ErrBodyTooLarge) {
		writeFastError(ctx, errHTTPTooLarge(), "")
		return
	}
	writeFastError(ctx, gatewayErrorBadRequest(err.Error()), "")
}
