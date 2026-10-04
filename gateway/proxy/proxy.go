// Package proxy is the Go implementation of the go-micro edge gateway
// (gateway/SPEC.md). It accepts gRPC calls, applies the rules (routing,
// IP restriction, JWT auth, rate limits) and forwards each call, as opaque
// frames, to a node of the target go-micro service.
//
// The transport is a transparent grpc-go proxy, which keeps upstream
// statuses, headers and trailers intact and controls exactly when a call
// may be retried. Everything else is go-micro: the registry and its cache,
// selector strategies and filters, config sources for the rules, and the
// logger.
//
//	gw, err := proxy.New(
//	    proxy.Registry(nacos.NewRegistry(registry.Addrs("127.0.0.1:8848"))),
//	    proxy.RulesFrom(src), // e.g. proxy.SourceFromURI("file:///etc/micro/gateway/rules.yaml")
//	)
//	...
//	gw.Serve(listener)
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flylib/go-micro/config/source"
	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/registry/cache"
	"github.com/flylib/go-micro/selector/p2c"
	mgrpc "github.com/flylib/go-micro/util/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Options configure a Gateway.
type Options struct {
	Registry       registry.Registry
	Rules          source.Source // nil: no rules document (RulesNone)
	TrustedProxies []netip.Prefix
	UpstreamTLS    *tls.Config // nil: plaintext h2c to nodes
	Logger         logger.Logger
}

type Option func(*Options)

// Registry sets the registry services are discovered in. Required.
func Registry(r registry.Registry) Option { return func(o *Options) { o.Registry = r } }

// RulesFrom sets the source the rules document is loaded and watched from.
func RulesFrom(s source.Source) Option { return func(o *Options) { o.Rules = s } }

// TrustedProxies sets the proxies whose x-forwarded-for is believed (SPEC 7).
func TrustedProxies(p ...netip.Prefix) Option {
	return func(o *Options) { o.TrustedProxies = p }
}

// UpstreamTLS dials nodes with TLS.
func UpstreamTLS(c *tls.Config) Option { return func(o *Options) { o.UpstreamTLS = c } }

// WithLogger sets the logger for access and reload logs.
func WithLogger(l logger.Logger) Option { return func(o *Options) { o.Logger = l } }

// Gateway is a running gateway.
type Gateway struct {
	opts         Options
	registry     registry.Registry // cached
	registryName string
	rules        atomic.Pointer[ruleSet]
	conns        *conns
	srv          *grpc.Server
	log          logger.Logger

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// New builds a gateway and loads its rules. A missing or invalid rules
// document is an error: the gateway does not start without valid rules
// (SPEC 10).
func New(opts ...Option) (*Gateway, error) {
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	if o.Registry == nil {
		return nil, errors.New("gateway: a registry is required")
	}
	if o.Logger == nil {
		o.Logger = logger.DefaultLogger
	}
	g := &Gateway{
		opts:         o,
		registryName: o.Registry.String(),
		registry:     cache.New(o.Registry),
		conns:        newConns(o.UpstreamTLS),
		log:          o.Logger,
		stop:         make(chan struct{}),
	}

	if o.Rules == nil {
		rs, err := parseRules([]byte("version: 1"), g.registryName)
		if err != nil {
			return nil, err
		}
		g.rules.Store(rs)
	} else {
		cs, err := o.Rules.Read()
		if err != nil {
			return nil, fmt.Errorf("gateway: read rules from %s: %w", o.Rules, err)
		}
		rs, err := parseRules(cs.Data, g.registryName)
		if err != nil {
			return nil, fmt.Errorf("gateway: %w", err)
		}
		g.rules.Store(rs)
		g.wg.Add(1)
		go g.watchRules()
	}

	g.srv = grpc.NewServer(
		grpc.UnknownServiceHandler(g.handle),
		grpc.ForceServerCodec(rawCodec{name: "proto"}),
	)
	g.wg.Add(1)
	go g.sweepConns()
	return g, nil
}

// Serve accepts calls on l until Stop.
func (g *Gateway) Serve(l net.Listener) error { return g.srv.Serve(l) }

// Stop drains in-flight calls and releases resources.
func (g *Gateway) Stop() {
	g.stopOnce.Do(func() {
		close(g.stop)
		g.srv.GracefulStop()
		g.wg.Wait()
		g.conns.close()
		if c, ok := g.registry.(cache.Cache); ok {
			c.Stop()
		}
	})
}

// watchRules applies every valid change of the rules source. An invalid
// document is logged and ignored; the previous rules stay (SPEC 10). A
// periodic re-read covers watch events a backend may drop.
func (g *Gateway) watchRules() {
	defer g.wg.Done()
	resync := time.NewTicker(30 * time.Second)
	defer resync.Stop()

	changes := make(chan []byte)
	go func() {
		backoff := time.Second
		for {
			w, err := g.opts.Rules.Watch()
			if err == nil {
				backoff = time.Second
				for {
					cs, err := w.Next()
					if err != nil {
						g.log.Logf(logger.WarnLevel, "gateway: rules watch on %s: %v", g.opts.Rules, err)
						break
					}
					select {
					case changes <- cs.Data:
					case <-g.stop:
						_ = w.Stop()
						return
					}
				}
				_ = w.Stop()
			} else {
				g.log.Logf(logger.WarnLevel, "gateway: rules watch on %s: %v", g.opts.Rules, err)
			}
			select {
			case <-g.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
		}
	}()

	var last []byte
	for {
		var data []byte
		select {
		case <-g.stop:
			return
		case data = <-changes:
		case <-resync.C:
			cs, err := g.opts.Rules.Read()
			if err != nil {
				continue
			}
			data = cs.Data
		}
		if string(data) == string(last) {
			continue
		}
		last = data
		rs, err := parseRules(data, g.registryName)
		if err != nil {
			g.log.Logf(logger.ErrorLevel, "gateway: rejected rules update, keeping previous rules: %v", err)
			continue
		}
		g.rules.Store(rs)
		g.log.Logf(logger.InfoLevel, "gateway: rules reloaded from %s", g.opts.Rules)
	}
}

func (g *Gateway) sweepConns() {
	defer g.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-g.stop:
			return
		case now := <-t.C:
			g.conns.sweep(now)
		}
	}
}

// handle serves every inbound call (grpc.UnknownServiceHandler).
func (g *Gateway) handle(_ any, in grpc.ServerStream) (err error) {
	ctx := in.Context()
	start := time.Now()
	method, _ := grpc.MethodFromServerStream(in)
	md, _ := metadata.FromIncomingContext(ctx)
	p, _ := peer.FromContext(ctx)
	c := &call{ctx: ctx, method: method, md: md, clientIP: clientIP(addrOf(p), md, g.opts.TrustedProxies)}

	var rt *route
	var service, node string
	defer func() { g.access(c, rt, service, node, start, err) }()

	// SPEC 3.1: malformed paths are rejected before anything else
	if _, _, perr := mgrpc.ServiceMethod(method); perr != nil || strings.Count(method, "/") != 2 {
		return errMalformedPath(method)
	}
	derived := mgrpc.ServiceFromMethod(method)

	rs := g.rules.Load()
	rt = rs.match(method, derived)
	if rt == nil {
		return errNoRoute(method)
	}
	service = rt.service
	if service == "" {
		service = derived
	}
	if service == "" {
		return errNoRoute(method)
	}

	for _, pl := range rs.global {
		if err := pl.check(c); err != nil {
			return err
		}
	}
	for _, pl := range rt.plugins {
		if err := pl.check(c); err != nil {
			return err
		}
	}

	services, gerr := g.registry.GetService(service)
	if gerr != nil && !errors.Is(gerr, registry.ErrNotFound) {
		g.log.Logf(logger.WarnLevel, "gateway: registry lookup %s: %v", service, gerr)
	}
	services = eligible(services, rt.filters, endpointOf(method))
	if countNodes(services) == 0 {
		return errNoNodes(service)
	}

	out := outgoingMetadata(md, c.clientIP)
	if rt.name != "" {
		out.Set(hdrRoute, rt.name)
	}
	if c.account != "" {
		out.Set(hdrAccount, c.account)
	}
	c.trace = first(out, hdrTraceparent)
	return g.forward(ctx, in, method, service, contentSubtype(md), rt, services, out, &node)
}

// forward picks nodes until one accepts the call, then relays it. A node
// is retried only while nothing has been sent to it (SPEC 6).
func (g *Gateway) forward(ctx context.Context, in grpc.ServerStream, method, service, subtype string, rt *route,
	services []*registry.Service, out metadata.MD, chosen *string) error {

	upCtx, cancel := context.WithCancel(metadata.NewOutgoingContext(ctx, out))
	defer cancel()

	codec := rawCodec{name: subtype}
	next := picker(rt, services)
	var lastErr error
	for attempt := 0; attempt <= rt.retries; attempt++ {
		n := next()
		if n == nil {
			break
		}
		cc, err := g.conns.get(n.Address)
		if err != nil {
			lastErr = err
			continue
		}
		if err := ready(ctx, cc, rt.connect); err != nil {
			lastErr = fmt.Errorf("%s: %w", n.Address, err)
			continue
		}
		var done func()
		if rt.selector.Strategy == "p2c" && len(rt.selector.VersionWeights) == 0 {
			done = p2c.Track(n.Address)
		}
		up, err := cc.NewStream(upCtx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, method, grpc.ForceCodec(codec))
		if err != nil {
			if done != nil {
				done()
			}
			// the stream never opened: nothing reached the node
			if status.Code(err) == codes.Unavailable {
				lastErr = fmt.Errorf("%s: %w", n.Address, err)
				continue
			}
			return err
		}
		*chosen = n.Address
		err = relay(in, up, cancel, rt, service)
		if done != nil {
			done()
		}
		return err
	}
	if lastErr == nil {
		lastErr = errors.New("no node accepted the call")
	}
	return errUpstreamConnect(service, lastErr)
}

// relay pumps frames both ways until the upstream finishes, then copies
// its trailer. Upstream errors are returned unchanged (SPEC 8).
func relay(in grpc.ServerStream, up grpc.ClientStream, cancel context.CancelFunc, rt *route, service string) error {
	var readTimedOut, sendTimedOut atomic.Bool

	toUp := make(chan error, 1)
	go func() {
		for {
			f := &frame{}
			if err := in.RecvMsg(f); err != nil {
				toUp <- err // io.EOF: client finished sending
				return
			}
			t := time.AfterFunc(rt.send, func() { sendTimedOut.Store(true); cancel() })
			err := up.SendMsg(f)
			t.Stop()
			if err != nil {
				// the upstream ended; its status comes from RecvMsg
				toUp <- io.EOF
				return
			}
		}
	}()

	fromUp := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			f := &frame{}
			t := time.AfterFunc(rt.read, func() { readTimedOut.Store(true); cancel() })
			err := up.RecvMsg(f)
			t.Stop()
			if err != nil {
				fromUp <- err
				return
			}
			if i == 0 {
				if hdr, herr := up.Header(); herr == nil && len(hdr) > 0 {
					_ = in.SendHeader(hdr)
				}
			}
			if err := in.SendMsg(f); err != nil {
				cancel()
				fromUp <- err
				return
			}
		}
	}()

	for {
		select {
		case err := <-toUp:
			if errors.Is(err, io.EOF) {
				_ = up.CloseSend()
				toUp = nil // keep waiting for the upstream
				continue
			}
			// the client went away
			cancel()
			return err
		case err := <-fromUp:
			if hdr, herr := up.Header(); herr == nil && len(hdr) > 0 {
				_ = in.SendHeader(hdr) // trailers-only replies carry no frames
			}
			in.SetTrailer(up.Trailer())
			switch {
			case errors.Is(err, io.EOF):
				return nil
			case readTimedOut.Load(), sendTimedOut.Load():
				return errUpstreamTimeout(service)
			}
			return err
		}
	}
}

// contentSubtype keeps the inbound codec (application/grpc+json stays
// json) for the upstream call.
func contentSubtype(md metadata.MD) string {
	ct := first(md, "content-type")
	if sub, ok := strings.CutPrefix(ct, "application/grpc+"); ok && sub != "" {
		return sub
	}
	return "proto"
}

// endpointOf turns /pkg.Handler/Method into Handler.Method, go-micro's
// endpoint name (SPEC 5.2).
func endpointOf(method string) string {
	parts := strings.Split(strings.TrimPrefix(method, "/"), "/")
	if len(parts) != 2 {
		return ""
	}
	h := parts[0]
	if i := strings.LastIndex(h, "."); i >= 0 {
		h = h[i+1:]
	}
	return h + "." + parts[1]
}

func addrOf(p *peer.Peer) net.Addr {
	if p == nil {
		return nil
	}
	return p.Addr
}

// access writes one log line per call, with the field names
// wrapper/logging uses so gateway and service lines join on trace_id.
func (g *Gateway) access(c *call, rt *route, service, node string, start time.Time, err error) {
	code := status.Code(err)
	level := logger.InfoLevel
	switch code {
	case codes.OK:
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable, codes.DeadlineExceeded:
		level = logger.ErrorLevel
	default:
		level = logger.WarnLevel
	}
	if !g.log.Options().Level.Enabled(level) {
		return
	}
	fields := map[string]interface{}{
		"method":    c.method,
		"service":   service,
		"client_ip": c.clientIP,
		"duration":  time.Since(start).String(),
		"code":      code.String(),
	}
	if rt != nil && rt.name != "" {
		fields["route"] = rt.name
	}
	if node != "" {
		fields["upstream"] = node
	}
	if tp := strings.Split(c.trace, "-"); len(tp) == 4 {
		fields["trace_id"] = tp[1]
	}
	if err != nil {
		fields["error"] = status.Convert(err).Message()
	}
	g.log.Fields(fields).Log(level, "gateway "+c.method)
}
