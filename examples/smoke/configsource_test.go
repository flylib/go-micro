package smoke

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	capi "github.com/hashicorp/consul/api"
	clientv3 "go.etcd.io/etcd/client/v3"

	cconsul "github.com/flylib/go-micro/config/source/consul"
	cetcd "github.com/flylib/go-micro/config/source/etcd"
	cnacos "github.com/flylib/go-micro/config/source/nacos"
	"github.com/flylib/go-micro/config/source"
)

func reachable(t *testing.T, addr string) {
	t.Helper()
	if _, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		t.Skipf("%s not reachable: %v", addr, err)
	}
}

// expectChange waits for the watcher to deliver a changeset containing want.
func expectChange(t *testing.T, w source.Watcher, want string) {
	t.Helper()
	type res struct {
		cs  *source.ChangeSet
		err error
	}
	ch := make(chan res, 1)
	go func() { cs, err := w.Next(); ch <- res{cs, err} }()
	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("watch next: %v", out.err)
		}
		if string(out.cs.Data) != want {
			t.Fatalf("watch got %q want %q", out.cs.Data, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no watch event within 30s")
	}
}

func TestSmokeEtcdConfigSource(t *testing.T) {
	reachable(t, "127.0.0.1:2379")
	cli, err := clientv3.New(clientv3.Config{Endpoints: []string{"127.0.0.1:2379"}, DialTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx := context.Background()
	if _, err := cli.Put(ctx, "/smoke/config", "db: one"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	src := cetcd.NewSource(cetcd.WithAddress("127.0.0.1:2379"), cetcd.WithKey("/smoke/config"))
	cs, err := src.Read()
	if err != nil || string(cs.Data) != "db: one" {
		t.Fatalf("read: %v %q", err, cs)
	}

	w, err := src.Watch()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if _, err := cli.Put(ctx, "/smoke/config", "db: two"); err != nil {
		t.Fatal(err)
	}
	expectChange(t, w, "db: two")
}

func TestSmokeConsulConfigSource(t *testing.T) {
	reachable(t, "127.0.0.1:8500")
	cli, err := capi.NewClient(capi.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.KV().Put(&capi.KVPair{Key: "smoke/config", Value: []byte("db: one")}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	src := cconsul.NewSource(cconsul.WithAddress("127.0.0.1:8500"), cconsul.WithKey("smoke/config"))
	cs, err := src.Read()
	if err != nil || string(cs.Data) != "db: one" {
		t.Fatalf("read: %v %q", err, cs)
	}

	w, err := src.Watch()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	if _, err := cli.KV().Put(&capi.KVPair{Key: "smoke/config", Value: []byte("db: two")}, nil); err != nil {
		t.Fatal(err)
	}
	expectChange(t, w, "db: two")
}

// publishNacos publishes config via the nacos HTTP API.
func publishNacos(t *testing.T, dataId, content string) {
	t.Helper()
	rsp, err := http.PostForm("http://127.0.0.1:8848/nacos/v1/cs/configs", url.Values{
		"dataId": {dataId}, "group": {"DEFAULT_GROUP"}, "content": {content},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	rsp.Body.Close()
}

func TestSmokeNacosConfigSource(t *testing.T) {
	reachable(t, "127.0.0.1:8848")
	if !waitReady(t, "127.0.0.1:8848", 90*time.Second) {
		t.Skip("nacos never became ready")
	}
	publishNacos(t, "smoke.yaml", "db: one")
	time.Sleep(500 * time.Millisecond)

	src := cnacos.NewSource(cnacos.WithAddress("127.0.0.1:8848"), cnacos.WithDataId("smoke.yaml"))
	cs, err := src.Read()
	if err != nil || string(cs.Data) != "db: one" {
		t.Fatalf("read: %v %v", err, cs)
	}

	w, err := src.Watch()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	publishNacos(t, "smoke.yaml", "db: two")
	expectChange(t, w, "db: two")
}
