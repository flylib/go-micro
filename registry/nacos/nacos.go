// Package nacos provides a nacos registry for go-micro.
package nacos

import (
	"errors"
	"net"
	"strconv"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"

	"github.com/flylib/go-micro/registry"
)

const (
	metaVersionKey  = "micro.version"
	defaultPort     = 8848
	defaultPageSize = 100
)

type nacosRegistry struct {
	client    naming_client.INamingClient
	opts      registry.Options
	group     string
	namespace string
}

// NewRegistry returns a nacos-backed registry.
func NewRegistry(opts ...registry.Option) registry.Registry {
	r := &nacosRegistry{}
	if err := configure(r, opts...); err != nil {
		panic(err)
	}
	return r
}

func configure(r *nacosRegistry, opts ...registry.Option) error {
	for _, o := range opts {
		o(&r.opts)
	}

	r.group = "" // DEFAULT_GROUP
	if r.opts.Context != nil {
		if g, ok := r.opts.Context.Value(groupNameKey{}).(string); ok {
			r.group = g
		}
		if ns, ok := r.opts.Context.Value(namespaceKey{}).(string); ok {
			r.namespace = ns
		}
		// pre-built client wins
		if c, ok := r.opts.Context.Value(namingClientKey{}).(naming_client.INamingClient); ok {
			r.client = c
			return nil
		}
	}

	cc := constant.ClientConfig{
		NotLoadCacheAtStart: true,
		TimeoutMs:           5000,
		LogLevel:            "warn",
	}
	if r.opts.Context != nil {
		if v, ok := r.opts.Context.Value(clientConfigKey{}).(constant.ClientConfig); ok {
			cc = v
		}
	}
	if r.namespace != "" && cc.NamespaceId == "" {
		cc.NamespaceId = r.namespace
	}

	var scs []constant.ServerConfig
	if r.opts.Context != nil {
		if v, ok := r.opts.Context.Value(serverConfigsKey{}).([]constant.ServerConfig); ok {
			scs = v
		}
	}
	if len(scs) == 0 {
		addrs := r.opts.Addrs
		if len(addrs) == 0 {
			addrs = []string{"127.0.0.1:8848"}
		}
		for _, addr := range addrs {
			host, portStr, err := net.SplitHostPort(addr)
			port := uint64(defaultPort)
			if err != nil {
				host = addr
			} else if p, perr := strconv.ParseUint(portStr, 10, 64); perr == nil {
				port = p
			}
			scs = append(scs, constant.ServerConfig{IpAddr: host, Port: port})
		}
	}

	client, err := clients.NewNamingClient(vo.NacosClientParam{
		ClientConfig:  &cc,
		ServerConfigs: scs,
	})
	if err != nil {
		return err
	}
	r.client = client
	return nil
}

func (r *nacosRegistry) Init(opts ...registry.Option) error {
	return configure(r, opts...)
}

func (r *nacosRegistry) Options() registry.Options {
	return r.opts
}

func (r *nacosRegistry) Register(s *registry.Service, _ ...registry.RegisterOption) error {
	if s == nil || len(s.Nodes) == 0 {
		return errors.New("require at least one node")
	}
	for _, node := range s.Nodes {
		host, port, err := splitAddress(node.Address)
		if err != nil {
			return err
		}
		meta := make(map[string]string, len(node.Metadata)+1)
		for k, v := range node.Metadata {
			meta[k] = v
		}
		meta[metaVersionKey] = s.Version
		if _, err := r.client.RegisterInstance(vo.RegisterInstanceParam{
			Ip:          host,
			Port:        port,
			ServiceName: s.Name,
			GroupName:   r.group,
			Weight:      10,
			Enable:      true,
			Healthy:     true,
			Ephemeral:   true,
			Metadata:    meta,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *nacosRegistry) Deregister(s *registry.Service, _ ...registry.DeregisterOption) error {
	if s == nil || len(s.Nodes) == 0 {
		return errors.New("require at least one node")
	}
	var gerr error
	for _, node := range s.Nodes {
		host, port, err := splitAddress(node.Address)
		if err != nil {
			gerr = err
			continue
		}
		if _, err := r.client.DeregisterInstance(vo.DeregisterInstanceParam{
			Ip:          host,
			Port:        port,
			ServiceName: s.Name,
			GroupName:   r.group,
			Ephemeral:   true,
		}); err != nil {
			gerr = err
		}
	}
	return gerr
}

func (r *nacosRegistry) GetService(name string, _ ...registry.GetOption) ([]*registry.Service, error) {
	instances, err := r.client.SelectInstances(vo.SelectInstancesParam{
		ServiceName: name,
		GroupName:   r.group,
		HealthyOnly: true,
	})
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, registry.ErrNotFound
	}

	// group instances into one Service per version
	byVersion := map[string]*registry.Service{}
	var order []string
	for _, in := range instances {
		version := in.Metadata[metaVersionKey]
		svc, ok := byVersion[version]
		if !ok {
			svc = &registry.Service{Name: name, Version: version}
			byVersion[version] = svc
			order = append(order, version)
		}
		svc.Nodes = append(svc.Nodes, &registry.Node{
			Id:       in.InstanceId,
			Address:  net.JoinHostPort(in.Ip, strconv.FormatUint(in.Port, 10)),
			Metadata: in.Metadata,
		})
	}

	services := make([]*registry.Service, 0, len(order))
	for _, v := range order {
		services = append(services, byVersion[v])
	}
	return services, nil
}

func (r *nacosRegistry) ListServices(_ ...registry.ListOption) ([]*registry.Service, error) {
	var services []*registry.Service
	for page := uint32(1); ; page++ {
		list, err := r.client.GetAllServicesInfo(vo.GetAllServiceInfoParam{
			NameSpace: r.namespace,
			GroupName: r.group,
			PageNo:    page,
			PageSize:  defaultPageSize,
		})
		if err != nil {
			return nil, err
		}
		for _, name := range list.Doms {
			services = append(services, &registry.Service{Name: name})
		}
		if int64(len(services)) >= list.Count || len(list.Doms) == 0 {
			break
		}
	}
	return services, nil
}

func (r *nacosRegistry) Watch(opts ...registry.WatchOption) (registry.Watcher, error) {
	return newWatcher(r, opts...)
}

func (r *nacosRegistry) String() string {
	return "nacos"
}

// splitAddress parses "host:port" (or bare "host", defaulting the port).
func splitAddress(addr string) (string, uint64, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, defaultPort, nil
	}
	port, err := strconv.ParseUint(portStr, 10, 64)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
