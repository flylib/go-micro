package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	merr "github.com/flylib/go-micro/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	pb "google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// path builds the gRPC method path go-micro's client would use for
// service/handler/method (SPEC 3.1).
func path(service, handler, method string) string {
	return fmt.Sprintf("/%s.%s/%s", service, handler, method)
}

// client calls through one address with a plain gRPC client — no go-micro.
type client struct{ conn *grpc.ClientConn }

func dial(addr string) (*client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &client{conn: conn}, nil
}

func (c *client) close() error { return c.conn.Close() }

// call invokes a unary method and decodes the backend echo. md is sent as
// outgoing metadata (key, value pairs).
func (c *client) call(ctx context.Context, method string, req *pb.SimpleRequest, md ...string) (echo, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if len(md) > 0 {
		ctx = metadata.AppendToOutgoingContext(ctx, md...)
	}
	if req == nil {
		req = &pb.SimpleRequest{}
	}
	var rsp pb.SimpleResponse
	if err := c.conn.Invoke(ctx, method, req, &rsp); err != nil {
		return echo{}, err
	}
	return decodeEcho(rsp.GetPayload())
}

// serverStream asks for n replies on a server-streaming call.
func (c *client) serverStream(ctx context.Context, method string, n int) ([]echo, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	st, err := c.conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, method)
	if err != nil {
		return nil, err
	}
	req := &pb.StreamingOutputCallRequest{ResponseParameters: make([]*pb.ResponseParameters, n)}
	for i := range req.ResponseParameters {
		req.ResponseParameters[i] = &pb.ResponseParameters{}
	}
	if err := st.SendMsg(req); err != nil {
		return nil, err
	}
	if err := st.CloseSend(); err != nil {
		return nil, err
	}
	return recvAll(st)
}

// bidiStream sends n requests on a bidirectional call and reads the replies.
func (c *client) bidiStream(ctx context.Context, method string, n int) ([]echo, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	st, err := c.conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, method)
	if err != nil {
		return nil, err
	}
	for i := 0; i < n; i++ {
		if err := st.SendMsg(&pb.StreamingOutputCallRequest{}); err != nil {
			return nil, err
		}
	}
	if err := st.CloseSend(); err != nil {
		return nil, err
	}
	return recvAll(st)
}

func recvAll(st grpc.ClientStream) ([]echo, error) {
	var out []echo
	for {
		var rsp pb.StreamingOutputCallResponse
		if err := st.RecvMsg(&rsp); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		e, err := decodeEcho(rsp.GetPayload())
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
}

func decodeEcho(p *pb.Payload) (echo, error) {
	var e echo
	if p == nil {
		return e, errors.New("reply has no payload")
	}
	if err := json.Unmarshal(p.GetBody(), &e); err != nil {
		return e, fmt.Errorf("decode echo: %w", err)
	}
	return e, nil
}

// failure is the gRPC status of err plus the go-micro error carried in
// its message (SPEC 8), if it parses as one.
type failure struct {
	code  codes.Code
	micro *merr.Error
	raw   string
}

func failureOf(err error) failure {
	st := status.Convert(err)
	f := failure{code: st.Code(), raw: st.Message()}
	var e merr.Error
	if json.Unmarshal([]byte(st.Message()), &e) == nil && e.Code != 0 {
		f.micro = &e
	}
	return f
}

func (f failure) String() string {
	return fmt.Sprintf("grpc=%s message=%q", f.code, f.raw)
}
