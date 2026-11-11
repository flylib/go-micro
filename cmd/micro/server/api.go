package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/flylib/go-micro/client"
	"github.com/flylib/go-micro/cmd/micro/internal/caller"
	merr "github.com/flylib/go-micro/errors"
	"github.com/flylib/go-micro/metadata"
	"github.com/flylib/go-micro/registry"
)

// The dashboard's HTTP API: POST /api/{service}/{endpoint} calls the
// service with the JSON body and answers with its JSON reply. The endpoint
// is "Handler.Method" or "Handler/Method". Calls are made as `micro call`
// makes them (cmd/micro/internal/caller): gRPC+JSON to nodes on the gRPC
// server, the default client to others.

const (
	apiErrorID = "micro.api"
	maxAPIBody = 4 << 20
	apiPrefix  = "/api/"
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
// reply, on whichever server the service runs. Errors are go-micro errors.
func (a *apiCaller) call(ctx context.Context, service, endpoint string, body []byte, hdr http.Header) ([]byte, error) {
	if len(strings.TrimSpace(string(body))) != 0 && !json.Valid(body) {
		return nil, merr.BadRequest(apiErrorID, "request body is not JSON")
	}
	ctx = metadata.NewContext(ctx, callMetadata(hdr))
	return caller.Call(ctx, a.registry, a.client, service, endpoint, body)
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
