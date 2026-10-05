package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/codec/bytes"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/selector"
)

// The dashboard's HTTP API: POST /api/{service}/{endpoint} calls the
// service with the JSON body and answers with its JSON reply. The endpoint
// is "Handler.Method" or "Handler/Method". Calls go through the default
// client, as `micro call` does, so they reach services on the default
// RPC server; services on the gRPC server are reached through the edge
// gateways (gateway/proxy, gateway/openresty) instead.

const (
	apiErrorID  = "micro.api"
	maxAPIBody  = 4 << 20
	apiPrefix   = "/api/"
	grpcProtoID = "grpc"
)

// apiCaller calls services; a variable so tests can use their own client
// and registry.
type apiCaller struct {
	client   client.Client
	registry registry.Registry
}

func defaultCaller() *apiCaller {
	return &apiCaller{client: client.DefaultClient, registry: registry.DefaultRegistry}
}

// splitAPIPath turns /api/{service}/{Handler}/{Method} or
// /api/{service}/{Handler.Method} into service and endpoint.
func splitAPIPath(path string) (service, endpoint string, ok bool) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, apiPrefix), "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] != "" && parts[1] != "" && parts[2] != "":
		return parts[0], parts[1] + "." + parts[2], true
	case len(parts) == 2 && parts[0] != "" && strings.Count(parts[1], ".") == 1 &&
		!strings.HasPrefix(parts[1], ".") && !strings.HasSuffix(parts[1], "."):
		return parts[0], parts[1], true
	}
	return "", "", false
}

// serveAPI handles a request under /api/. check enforces endpoint scopes
// and writes the refusal itself.
func (a *apiCaller) serveAPI(w http.ResponseWriter, r *http.Request, check func(http.ResponseWriter, *http.Request, string) bool) {
	service, endpoint, ok := splitAPIPath(r.URL.Path)
	if !ok {
		writeAPIError(w, merr.NotFound(apiErrorID, "use POST /api/{service}/{Handler}/{Method}"))
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, merr.MethodNotAllowed(apiErrorID, "method %s not allowed", r.Method))
		return
	}
	if !check(w, r, service+"."+endpoint) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAPIBody))
	if err != nil {
		writeAPIError(w, merr.New(apiErrorID, "request body: "+err.Error(), http.StatusRequestEntityTooLarge))
		return
	}
	reply, err := a.call(r.Context(), service, endpoint, body, r.Header)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply)
}

// call sends a JSON request to a service endpoint and returns the JSON
// reply. Errors are go-micro errors.
func (a *apiCaller) call(ctx context.Context, service, endpoint string, body []byte, hdr http.Header) ([]byte, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		return nil, merr.BadRequest(apiErrorID, "request body is not JSON")
	}
	svcs, err := a.registry.GetService(service)
	if err != nil || len(svcs) == 0 {
		return nil, merr.NotFound(apiErrorID, "service %s not found", service)
	}
	if onlyGRPC(svcs) {
		return nil, merr.New(apiErrorID, fmt.Sprintf("service %s runs the gRPC server: call it through an edge gateway's HTTP entry (gateway/proxy, gateway/openresty)", service), http.StatusNotImplemented)
	}

	ctx = metadata.NewContext(ctx, callMetadata(hdr))
	req := a.client.NewRequest(service, endpoint, &bytes.Frame{Data: body}, client.WithContentType("application/json"))
	var rsp bytes.Frame
	// skip gRPC nodes of a service that also has nodes the client can reach
	if err := a.client.Call(ctx, req, &rsp, client.WithSelectOption(selector.WithFilter(withoutGRPC))); err != nil {
		if e := merr.Parse(err.Error()); e.Code > 0 {
			return nil, e
		}
		return nil, merr.InternalServerError(apiErrorID, "%s", err.Error())
	}
	if len(rsp.Data) == 0 {
		return []byte("{}"), nil
	}
	return rsp.Data, nil
}

// withoutGRPC drops nodes that run the gRPC server.
func withoutGRPC(svcs []*registry.Service) []*registry.Service {
	out := make([]*registry.Service, 0, len(svcs))
	for _, s := range svcs {
		cp := *s
		cp.Nodes = nil
		for _, n := range s.Nodes {
			if n.Metadata["protocol"] != grpcProtoID {
				cp.Nodes = append(cp.Nodes, n)
			}
		}
		if len(cp.Nodes) > 0 {
			out = append(out, &cp)
		}
	}
	return out
}

// onlyGRPC reports whether every node of a service runs the gRPC server,
// which the default client cannot reach.
func onlyGRPC(svcs []*registry.Service) bool {
	n := 0
	for _, s := range svcs {
		for _, node := range s.Nodes {
			if node.Metadata["protocol"] != grpcProtoID {
				return false
			}
			n++
		}
	}
	return n > 0
}

// callMetadata forwards request headers as call metadata, except the
// dashboard's own credentials and hop-by-hop headers.
func callMetadata(h http.Header) metadata.Metadata {
	md := metadata.Metadata{}
	for k, vs := range h {
		switch strings.ToLower(k) {
		case "cookie", "authorization", "connection", "content-length", "content-type",
			"accept-encoding", "host", "te", "trailer", "transfer-encoding", "upgrade", "keep-alive":
			continue
		}
		if len(vs) > 0 {
			md[k] = vs[0]
		}
	}
	return md
}

func writeAPIError(w http.ResponseWriter, err error) {
	e := merr.FromError(err)
	if e.Id == "" {
		e.Id = apiErrorID
	}
	code := int(e.Code)
	if code < 100 || code > 599 {
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(e)
}

// registerHealth serves /health, /health/live and /health/ready without
// authentication, for load balancers and orchestrators. Live means the
// gateway answers; ready also needs the registry to answer.
func registerHealth(mux *http.ServeMux, reg registry.Registry) {
	live := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
	ready := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		svcs, err := reg.ListServices()
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "unavailable", "registry": reg.String(), "error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "registry": reg.String(), "services": len(svcs)})
	}
	mux.HandleFunc("/health", ready)
	mux.HandleFunc("/health/ready", ready)
	mux.HandleFunc("/health/live", live)
}
