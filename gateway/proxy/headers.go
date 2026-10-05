package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/netip"
	"strings"

	"google.golang.org/grpc/metadata"
)

// Header names the gateway reads or writes (SPEC 7). gRPC metadata keys
// are lowercase.
const (
	hdrTraceparent    = "traceparent"
	hdrForwardedFor   = "x-forwarded-for"
	hdrReservedPrefix = "micro-gateway-"
	hdrAccount        = "micro-gateway-account"
	hdrRoute          = "micro-gateway-route"
	hdrAuthorization  = "authorization"
)

// hopHeaders are set by grpc-go itself on the upstream call and must not
// be copied from the inbound call.
var hopHeaders = map[string]bool{
	":authority":   true,
	"content-type": true,
	"user-agent":   true,
	"te":           true,
	"grpc-timeout": true, // re-derived from the context deadline
}

// outgoingMetadata builds the upstream metadata from the inbound call:
// everything passes through except hop headers and the reserved
// micro-gateway-* prefix, which only the gateway may set.
func outgoingMetadata(in metadata.MD, clientIP string) metadata.MD {
	out := metadata.MD{}
	for k, v := range in {
		if hopHeaders[k] || strings.HasPrefix(k, hdrReservedPrefix) {
			continue
		}
		out[k] = append([]string(nil), v...)
	}

	// append the client to the forwarding chain
	if xff := strings.Join(in.Get(hdrForwardedFor), ", "); xff != "" {
		out.Set(hdrForwardedFor, xff+", "+clientIP)
	} else {
		out.Set(hdrForwardedFor, clientIP)
	}

	if !validTraceparent(first(in, hdrTraceparent)) {
		out.Set(hdrTraceparent, newTraceparent())
	}
	return out
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// validTraceparent accepts W3C version-00 values with non-zero ids.
func validTraceparent(v string) bool {
	parts := strings.Split(v, "-")
	if len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return false
	}
	for _, p := range parts[1:] {
		if !isLowerHex(p) {
			return false
		}
	}
	return strings.Trim(parts[1], "0") != "" && strings.Trim(parts[2], "0") != ""
}

func isLowerHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// newTraceparent starts a sampled trace for a call that arrived without one.
func newTraceparent() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return "00-" + hex.EncodeToString(b[:16]) + "-" + hex.EncodeToString(b[16:]) + "-01"
}

// clientIP is the peer address, unless the peer is a trusted proxy: then
// it is the right-most x-forwarded-for entry that is not itself trusted
// (SPEC 7).
func clientIP(ip string, in metadata.MD, trusted []netip.Prefix) string {
	if !isTrusted(ip, trusted) {
		return ip
	}
	hops := strings.Split(strings.Join(in.Get(hdrForwardedFor), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := strings.TrimSpace(hops[i])
		if h == "" {
			continue
		}
		if !isTrusted(h, trusted) {
			return h
		}
	}
	return ip
}

func hostOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return host
}

func isTrusted(ip string, trusted []netip.Prefix) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
