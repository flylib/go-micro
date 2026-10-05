package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"

	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/gateway/push"
	"github.com/flylib/go-micro/logger"
)

// The WebSocket entry (SPEC 2.3): GET /ws on the HTTP entry. One
// connection carries calls, streams and topic subscriptions as JSON
// messages, and receives pushes to its account. Each call and stream goes
// through the same route, plugins, discovery and retries as a call on the
// gRPC entry.

const (
	wsPath         = "/ws"
	wsSubprotocol  = "micro.v1"
	wsMaxMessage   = maxHTTPBody
	wsDefaultCalls = 100
	wsQueue        = 256 // outgoing messages buffered per connection
	wsPingEvery    = 30 * time.Second
	wsPingTimeout  = 30 * time.Second
	wsWriteTimeout = 10 * time.Second
	wsStreamQueue  = 64 // client messages buffered per stream
)

// wsRules is the compiled websocket section (SPEC 9.7).
type wsRules struct {
	plugins  []plugin
	topics   []topicPattern
	maxCalls int
}

func compileWS(spec WebSocketSpec) (*wsRules, error) {
	plugins, err := buildPlugins(spec.Plugins)
	if err != nil {
		return nil, err
	}
	w := &wsRules{plugins: plugins, maxCalls: spec.MaxCalls}
	if w.maxCalls == 0 {
		w.maxCalls = wsDefaultCalls
	}
	for _, t := range spec.Topics {
		w.topics = append(w.topics, topicPattern(strings.Split(t, ".")))
	}
	return w, nil
}

// topicPattern is a dot-separated topic pattern: "*" matches one segment,
// a final ">" one or more, and {account} the connection's account.
type topicPattern []string

func (p topicPattern) match(topic []string, account string) bool {
	for i, seg := range p {
		if seg == ">" && i == len(p)-1 {
			return len(topic) > i
		}
		if i >= len(topic) {
			return false
		}
		switch {
		case seg == "*":
		case strings.Contains(seg, "{account}"):
			if account == "" || !push.ValidTopic(account) || strings.Contains(account, ".") {
				return false
			}
			if strings.ReplaceAll(seg, "{account}", account) != topic[i] {
				return false
			}
		case seg != topic[i]:
			return false
		}
	}
	return len(topic) == len(p)
}

func (w *wsRules) allows(topic, account string) bool {
	segs := strings.Split(topic, ".")
	for _, p := range w.topics {
		if p.match(segs, account) {
			return true
		}
	}
	return false
}

// wsIn is a client message.
type wsIn struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Method    string            `json:"method"`
	Body      json.RawMessage   `json:"body"`
	Metadata  map[string]string `json:"metadata"`
	CloseSend bool              `json:"close_send"`
	Topic     string            `json:"topic"`
}

// wsOut is a gateway message.
type wsOut struct {
	ID    string          `json:"id,omitempty"`
	Type  string          `json:"type"`
	Topic string          `json:"topic,omitempty"`
	Body  json.RawMessage `json:"body,omitempty"`
	Error json.RawMessage `json:"error,omitempty"`
}

// serveWS upgrades GET /ws when the rules have a websocket section.
func (g *Gateway) serveWS(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rs := g.rules.Load()
	if rs.ws == nil {
		writeHTTPError(w, errHTTPNotFound(r.URL.Path), "")
		return
	}
	md := wsMetadata(r)
	c := &call{
		ctx:      r.Context(),
		entry:    "ws",
		method:   wsPath,
		md:       md,
		clientIP: clientIP(hostOfString(r.RemoteAddr), md, g.opts.TrustedProxies),
	}
	var err error
	defer func() { g.access(c, nil, "", start, err) }()

	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		err = errHTTPMethod(r.Method)
		writeHTTPError(w, err, "")
		return
	}
	for _, pl := range rs.ws.plugins {
		if err = pl.check(c); err != nil {
			writeHTTPError(w, err, "")
			return
		}
	}
	ws, aerr := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{wsSubprotocol},
		// clients authenticate with tokens, not cookies, so any origin
		// may connect (browsers on the app's own domain included)
		OriginPatterns: []string{"*"},
	})
	if aerr != nil {
		err = gatewayErrorBadRequest("websocket upgrade: " + aerr.Error())
		return // Accept has answered the request
	}
	ws.SetReadLimit(wsMaxMessage)

	conn := &wsConn{
		g:        g,
		ws:       ws,
		md:       md,
		clientIP: c.clientIP,
		account:  c.account,
		forward:  c.forward,
		out:      make(chan []byte, wsQueue),
		calls:    map[string]*wsCall{},
		subs:     map[string]bool{},
	}
	go conn.run()
}

// wsMetadata turns the handshake into connection metadata (SPEC 2.3).
func wsMetadata(r *http.Request) metadata.MD {
	md := httpMetadata(r.Header)
	for k := range md {
		if strings.HasPrefix(k, "sec-websocket-") {
			delete(md, k)
		}
	}
	if tok := r.URL.Query().Get("access_token"); tok != "" && len(md["authorization"]) == 0 {
		md.Set("authorization", "Bearer "+tok)
	}
	return md
}

// wsConn is one open WebSocket connection.
type wsConn struct {
	g        *Gateway
	ws       *websocket.Conn
	md       metadata.MD
	clientIP string
	account  string
	forward  map[string]string

	ctx    context.Context
	cancel context.CancelFunc
	out    chan []byte

	mu    sync.Mutex
	calls map[string]*wsCall
	subs  map[string]bool // client topics
}

// wsCall is an open call or stream.
type wsCall struct {
	ctx      context.Context
	cancel   context.CancelFunc
	canceled bool // by the client; guarded by wsConn.mu

	mu     sync.Mutex           // guards send and closed
	send   chan json.RawMessage // streams only; closed by close_send
	closed bool
}

func (c *wsConn) run() {
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.g.wsAdd(c)
	defer c.g.wsRemove(c)

	if c.account != "" && c.g.hub != nil {
		if err := c.g.hub.join(push.UserTopic(c.account), c); err != nil {
			c.g.log.Logf(logger.ErrorLevel, "gateway: websocket push subscription for %s: %v", c.account, err)
		}
	}

	go c.writer()
	go c.pinger()
	status, reason := c.reader()

	c.cancel()
	if c.g.hub != nil {
		c.g.hub.leaveAll(c)
	}
	_ = c.ws.Close(status, reason)
}

// reader handles client messages until the connection ends, and returns
// the close status to send.
func (c *wsConn) reader() (websocket.StatusCode, string) {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			// the client closed, the connection broke, or the message was
			// over the read limit (the library has closed with 1009)
			return websocket.StatusNormalClosure, ""
		}
		if typ != websocket.MessageText {
			return websocket.StatusUnsupportedData, "binary messages are not supported"
		}
		var m wsIn
		if err := json.Unmarshal(data, &m); err != nil {
			c.fail(peekID(data), gatewayErrorBadRequest("malformed message: "+err.Error()), "")
			continue
		}
		c.handle(&m)
	}
}

// peekID reads the id of a message that does not decode as a whole.
func peekID(data []byte) string {
	var m struct {
		ID any `json:"id"`
	}
	if json.Unmarshal(data, &m) == nil {
		if s, ok := m.ID.(string); ok {
			return s
		}
	}
	return ""
}

func (c *wsConn) handle(m *wsIn) {
	switch m.Type {
	case "call", "stream":
		c.open(m)
	case "send":
		c.streamSend(m)
	case "close_send":
		c.streamCloseSend(m)
	case "cancel":
		c.mu.Lock()
		if wc, ok := c.calls[m.ID]; ok {
			wc.canceled = true
			wc.cancel()
		}
		c.mu.Unlock()
	case "subscribe":
		c.subscribe(m)
	case "unsubscribe":
		c.unsubscribe(m)
	case "ping":
		c.queue(&wsOut{ID: m.ID, Type: "pong"})
	default:
		c.fail(m.ID, gatewayErrorBadRequest("unknown message type "+quote(m.Type)), "")
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// open starts a call or stream.
func (c *wsConn) open(m *wsIn) {
	if m.ID == "" {
		c.fail("", gatewayErrorBadRequest(m.Type+" without an id"), "")
		return
	}
	if m.Method == "" {
		c.fail(m.ID, gatewayErrorBadRequest(m.Type+" without a method"), "")
		return
	}
	rs := c.g.rules.Load()
	limit := wsDefaultCalls
	if rs.ws != nil {
		limit = rs.ws.maxCalls
	}
	ctx, cancel := context.WithCancel(c.ctx)
	wc := &wsCall{ctx: ctx, cancel: cancel}
	if m.Type == "stream" {
		wc.send = make(chan json.RawMessage, wsStreamQueue)
	}
	c.mu.Lock()
	_, dup := c.calls[m.ID]
	n := len(c.calls)
	if !dup && n < limit {
		c.calls[m.ID] = wc
	}
	c.mu.Unlock()
	switch {
	case dup:
		cancel()
		c.fail(m.ID, gatewayErrorBadRequest("id "+quote(m.ID)+" is already in use"), "")
		return
	case n >= limit:
		cancel()
		c.fail(m.ID, gatewayError(codes.ResourceExhausted, http.StatusTooManyRequests, "too many open calls on this connection"), "")
		return
	}

	cl := &call{
		ctx:      ctx,
		entry:    "ws",
		method:   m.Method,
		md:       c.callMetadata(m.Metadata),
		clientIP: c.clientIP,
		account:  c.account,
		forward:  copyMap(c.forward),
	}
	if m.Type == "call" {
		go c.unary(ctx, wc, m.ID, cl, m.Body)
		return
	}
	if len(m.Body) > 0 {
		wc.send <- m.Body
	}
	if m.CloseSend {
		wc.closed = true
		close(wc.send)
	}
	go c.stream(ctx, wc, m.ID, cl)
}

// callMetadata is the connection's metadata plus the call's own.
func (c *wsConn) callMetadata(extra map[string]string) metadata.MD {
	md := c.md.Copy()
	for k, v := range extra {
		k = strings.ToLower(k)
		if validMetadataKey(k) {
			md.Set(k, v)
		}
	}
	return md
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// finish removes a call and reports whether the client canceled it.
func (c *wsConn) finish(id string, wc *wsCall) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls[id] == wc {
		delete(c.calls, id)
	}
	return wc.canceled
}

func (c *wsConn) unary(ctx context.Context, wc *wsCall, id string, cl *call, body json.RawMessage) {
	start := time.Now()
	var pc *prepared
	var node string
	var err error
	defer func() { c.g.access(cl, pc, node, start, err) }()
	defer wc.cancel()

	var reply *frame
	pc, reply, _, err = c.g.callJSON(ctx, cl, body, &node)
	if c.finish(id, wc) {
		err = errCanceled()
		c.fail(id, err, "")
		return
	}
	if err != nil {
		c.fail(id, err, serviceOf(pc))
		return
	}
	c.queueWait(ctx, &wsOut{ID: id, Type: "reply", Body: rawJSON(reply.data)})
}

func (c *wsConn) stream(ctx context.Context, wc *wsCall, id string, cl *call) {
	start := time.Now()
	var pc *prepared
	var node string
	var err error
	defer func() { c.g.access(cl, pc, node, start, err) }()
	defer wc.cancel()

	end := func() {
		canceled := c.finish(id, wc)
		switch {
		case canceled:
			err = errCanceled()
			c.fail(id, err, "")
		case err != nil:
			c.fail(id, err, serviceOf(pc))
		default:
			c.queueWait(ctx, &wsOut{ID: id, Type: "end"})
		}
	}
	if pc, err = c.g.prepare(cl); err != nil {
		end()
		return
	}
	up, cancel, done, oerr := c.g.open(ctx, pc, "json", &node)
	if oerr != nil {
		err = oerr
		end()
		return
	}
	defer cancel()
	defer done()

	rt := pc.route
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case body, ok := <-wc.send:
				if !ok {
					_ = up.CloseSend()
					return
				}
				if len(body) == 0 {
					body = json.RawMessage("{}")
				}
				t := time.AfterFunc(rt.send, cancel)
				serr := up.SendMsg(&frame{data: body})
				t.Stop()
				if serr != nil {
					return // the upstream ended; RecvMsg has its status
				}
			}
		}
	}()

	var timedOut atomic.Bool
	for {
		f := &frame{}
		t := time.AfterFunc(rt.read, func() { timedOut.Store(true); cancel() })
		rerr := up.RecvMsg(f)
		t.Stop()
		if errors.Is(rerr, io.EOF) {
			end()
			return
		}
		if rerr != nil {
			err = rerr
			if timedOut.Load() {
				err = errUpstreamTimeout(pc.service)
			}
			end()
			return
		}
		c.queueWait(ctx, &wsOut{ID: id, Type: "message", Body: rawJSON(f.data)})
	}
}

func (c *wsConn) streamSend(m *wsIn) {
	c.mu.Lock()
	wc, ok := c.calls[m.ID]
	c.mu.Unlock()
	if !ok || wc.send == nil {
		c.fail(m.ID, gatewayErrorBadRequest("no open stream "+quote(m.ID)), "")
		return
	}
	wc.mu.Lock()
	defer wc.mu.Unlock()
	if wc.closed {
		c.fail(m.ID, gatewayErrorBadRequest("stream "+quote(m.ID)+" was closed for sending"), "")
		return
	}
	// waits while the stream's queue is full; ends with the stream
	select {
	case wc.send <- m.Body:
	case <-wc.ctx.Done():
	}
}

func (c *wsConn) streamCloseSend(m *wsIn) {
	c.mu.Lock()
	wc, ok := c.calls[m.ID]
	c.mu.Unlock()
	if !ok || wc.send == nil {
		return // finished already, or not a stream: nothing to close
	}
	wc.mu.Lock()
	if !wc.closed {
		wc.closed = true
		close(wc.send)
	}
	wc.mu.Unlock()
}

func (c *wsConn) subscribe(m *wsIn) {
	switch {
	case c.g.hub == nil:
		c.fail(m.ID, gatewayError(codes.Unavailable, http.StatusServiceUnavailable, "topics need a broker"), "")
		return
	case !push.ValidTopic(m.Topic):
		c.fail(m.ID, gatewayErrorBadRequest("invalid topic "+quote(m.Topic)), "")
		return
	}
	rs := c.g.rules.Load()
	if rs.ws == nil || !rs.ws.allows(m.Topic, c.account) {
		c.fail(m.ID, errForbidden("topic "+quote(m.Topic)+" is not allowed"), "")
		return
	}
	c.mu.Lock()
	had := c.subs[m.Topic]
	c.subs[m.Topic] = true
	c.mu.Unlock()
	if !had {
		if err := c.g.hub.join(push.TopicOf(m.Topic), c); err != nil {
			c.mu.Lock()
			delete(c.subs, m.Topic)
			c.mu.Unlock()
			c.fail(m.ID, gatewayError(codes.Unavailable, http.StatusServiceUnavailable, "subscribe: "+err.Error()), "")
			return
		}
	}
	c.queue(&wsOut{ID: m.ID, Type: "subscribed", Topic: m.Topic})
}

func (c *wsConn) unsubscribe(m *wsIn) {
	c.mu.Lock()
	had := c.subs[m.Topic]
	delete(c.subs, m.Topic)
	c.mu.Unlock()
	if had && c.g.hub != nil {
		c.g.hub.leave(push.TopicOf(m.Topic), c)
	}
	c.queue(&wsOut{ID: m.ID, Type: "unsubscribed", Topic: m.Topic})
}

func errCanceled() error {
	return gatewayError(codes.Canceled, 499, "canceled by the client")
}

// fail sends an error message built like an HTTP/JSON error (SPEC 2.1).
func (c *wsConn) fail(id string, err error, service string) {
	_, body := httpError(err, service)
	c.queue(&wsOut{ID: id, Type: "error", Error: body})
}

// rawJSON returns b as a JSON value; a reply that is not JSON (a service
// answering with another codec) is sent as a string.
func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	if json.Valid(b) {
		return b
	}
	s, _ := json.Marshal(string(b))
	return s
}

// queue sends a message without waiting: when the client has stopped
// reading and the queue is full, the connection is closed (SPEC 2.3).
func (c *wsConn) queue(m *wsOut) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	select {
	case c.out <- b:
	case <-c.ctx.Done():
	default:
		c.g.log.Logf(logger.WarnLevel, "gateway: websocket client %s too slow, closing", c.clientIP)
		go func() { _ = c.ws.Close(websocket.StatusPolicyViolation, "client too slow") }()
		c.cancel()
	}
}

// queueWait sends a call's message, waiting for room: a slow client slows
// its own calls down instead of losing their messages.
func (c *wsConn) queueWait(ctx context.Context, m *wsOut) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	select {
	case c.out <- b:
	case <-c.ctx.Done():
	case <-ctx.Done():
		// canceled while waiting: the error message still goes out
		select {
		case c.out <- b:
		case <-c.ctx.Done():
		}
	}
}

// deliver hands a broker message to the connection (hub callback).
func (c *wsConn) deliver(m *wsOut) { c.queue(m) }

func (c *wsConn) writer() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.out:
			ctx, cancel := context.WithTimeout(c.ctx, wsWriteTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

func (c *wsConn) pinger() {
	t := time.NewTicker(wsPingEvery)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, wsPingTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				c.cancel()
				return
			}
		}
	}
}

// wsAdd and wsRemove track open connections, so Stop can close them.
func (g *Gateway) wsAdd(c *wsConn) {
	g.wsMu.Lock()
	if g.wsConns == nil {
		g.wsConns = map[*wsConn]bool{}
	}
	g.wsConns[c] = true
	g.wsMu.Unlock()
}

func (g *Gateway) wsRemove(c *wsConn) {
	g.wsMu.Lock()
	delete(g.wsConns, c)
	g.wsMu.Unlock()
}

// closeWS closes every open connection with "going away".
func (g *Gateway) closeWS() {
	g.wsMu.Lock()
	conns := make([]*wsConn, 0, len(g.wsConns))
	for c := range g.wsConns {
		conns = append(conns, c)
	}
	g.wsMu.Unlock()
	for _, c := range conns {
		_ = c.ws.Close(websocket.StatusGoingAway, "gateway stopping")
		if c.cancel != nil {
			c.cancel()
		}
	}
}

// hub subscribes to broker topics on behalf of connections: one broker
// subscription per topic, shared by every connection that needs it.
type hub struct {
	b   broker.Broker
	log logger.Logger

	mu     sync.Mutex
	topics map[string]*hubTopic
}

type hubTopic struct {
	sub   broker.Subscriber
	conns map[*wsConn]bool
}

func newHub(b broker.Broker, log logger.Logger) *hub {
	return &hub{b: b, log: log, topics: map[string]*hubTopic{}}
}

func (h *hub) join(topic string, c *wsConn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.topics[topic]; ok {
		t.conns[c] = true
		return nil
	}
	sub, err := h.b.Subscribe(topic, func(e broker.Event) error {
		h.dispatch(topic, e.Message())
		return nil
	})
	if err != nil {
		return err
	}
	h.topics[topic] = &hubTopic{sub: sub, conns: map[*wsConn]bool{c: true}}
	return nil
}

func (h *hub) leave(topic string, c *wsConn) {
	h.mu.Lock()
	t, ok := h.topics[topic]
	if !ok {
		h.mu.Unlock()
		return
	}
	delete(t.conns, c)
	last := len(t.conns) == 0
	if last {
		delete(h.topics, topic)
	}
	h.mu.Unlock()
	if last {
		if err := t.sub.Unsubscribe(); err != nil {
			h.log.Logf(logger.WarnLevel, "gateway: unsubscribe %s: %v", topic, err)
		}
	}
}

// leaveAll drops every subscription of a closing connection.
func (h *hub) leaveAll(c *wsConn) {
	h.mu.Lock()
	var topics []string
	for name, t := range h.topics {
		if t.conns[c] {
			topics = append(topics, name)
		}
	}
	h.mu.Unlock()
	for _, t := range topics {
		h.leave(t, c)
	}
}

func (h *hub) dispatch(topic string, m *broker.Message) {
	if m == nil || !json.Valid(m.Body) {
		h.log.Logf(logger.WarnLevel, "gateway: dropped a non-JSON message on %s", topic)
		return
	}
	out := &wsOut{Type: "push", Body: m.Body}
	if t, ok := strings.CutPrefix(topic, push.TopicPrefix); ok {
		out = &wsOut{Type: "event", Topic: t, Body: m.Body}
	}
	h.mu.Lock()
	t, ok := h.topics[topic]
	var conns []*wsConn
	if ok {
		for c := range t.conns {
			conns = append(conns, c)
		}
	}
	h.mu.Unlock()
	for _, c := range conns {
		c.deliver(out)
	}
}
