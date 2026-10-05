package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	consul "github.com/hashicorp/consul/api"
	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// rules is a rules document (gateway/rules.schema.json) as plain maps, so
// tests can write exactly the JSON the spec defines. JSON is valid YAML,
// which every rules source accepts.
type rules map[string]any

type route map[string]any

// rulesWriter publishes a rules document to the source named by
// MICRO_GATEWAY_RULES (SPEC 11).
type rulesWriter interface {
	write(ctx context.Context, doc []byte) error
	close() error
}

func newRulesWriter(uri string) (rulesWriter, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("MICRO_GATEWAY_RULES: %w", err)
	}
	key := strings.TrimPrefix(u.Path, "/")
	switch u.Scheme {
	case "file":
		return fileWriter{path: u.Path}, nil
	case "etcd":
		cli, err := clientv3.New(clientv3.Config{Endpoints: []string{u.Host}, DialTimeout: 5 * time.Second})
		if err != nil {
			return nil, err
		}
		return etcdWriter{cli: cli, key: "/" + key}, nil
	case "consul":
		cfg := consul.DefaultConfig()
		cfg.Address = u.Host
		cli, err := consul.NewClient(cfg)
		if err != nil {
			return nil, err
		}
		return consulWriter{kv: cli.KV(), key: key}, nil
	case "nacos":
		return newNacosWriter(u, key)
	default:
		return nil, fmt.Errorf("MICRO_GATEWAY_RULES: unsupported scheme %q (want file, etcd, consul or nacos)", u.Scheme)
	}
}

type fileWriter struct{ path string }

// write replaces the file atomically, as a deploy tool would, so the
// gateway never reads a half-written document.
func (w fileWriter) write(_ context.Context, doc []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".rules-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(doc); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	// CreateTemp makes the file 0600; a gateway running as another user
	// must be able to read the rules
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), w.path)
}

func (fileWriter) close() error { return nil }

type etcdWriter struct {
	cli *clientv3.Client
	key string
}

func (w etcdWriter) write(ctx context.Context, doc []byte) error {
	_, err := w.cli.Put(ctx, w.key, string(doc))
	return err
}

func (w etcdWriter) close() error { return w.cli.Close() }

type consulWriter struct {
	kv  *consul.KV
	key string
}

func (w consulWriter) write(ctx context.Context, doc []byte) error {
	_, err := w.kv.Put(&consul.KVPair{Key: w.key, Value: doc}, (&consul.WriteOptions{}).WithContext(ctx))
	return err
}

func (consulWriter) close() error { return nil }

type nacosWriter struct {
	cli           config_client.IConfigClient
	dataID, group string
}

func newNacosWriter(u *url.URL, dataID string) (rulesWriter, error) {
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("MICRO_GATEWAY_RULES: nacos host: %w", err)
	}
	port, err := strconv.ParseUint(portStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("MICRO_GATEWAY_RULES: nacos port: %w", err)
	}
	group := u.Query().Get("group")
	if group == "" {
		group = "DEFAULT_GROUP"
	}
	cc := constant.ClientConfig{
		NamespaceId:         u.Query().Get("namespace"),
		TimeoutMs:           5000,
		NotLoadCacheAtStart: true,
		LogLevel:            "warn",
	}
	// credentials from the URI, else the registry's, as the gateways do
	if u.User != nil {
		cc.Username = u.User.Username()
		cc.Password, _ = u.User.Password()
	} else if user := os.Getenv("MICRO_REGISTRY_USERNAME"); user != "" {
		cc.Username, cc.Password = user, os.Getenv("MICRO_REGISTRY_PASSWORD")
	}
	cli, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: []constant.ServerConfig{{IpAddr: host, Port: port}},
	})
	if err != nil {
		return nil, err
	}
	return nacosWriter{cli: cli, dataID: dataID, group: group}, nil
}

func (w nacosWriter) write(_ context.Context, doc []byte) error {
	ok, err := w.cli.PublishConfig(vo.ConfigParam{DataId: w.dataID, Group: w.group, Content: string(doc), Type: "json"})
	if err == nil && !ok {
		err = fmt.Errorf("nacos refused config %s/%s", w.group, w.dataID)
	}
	return err
}

func (w nacosWriter) close() error {
	w.cli.CloseClient()
	return nil
}

func encodeRules(r rules) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
