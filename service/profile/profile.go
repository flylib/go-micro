// Package profile provides grouped plugin profiles for go-micro
package profile

import (
	"github.com/flylib/go-micro/broker"
	"github.com/flylib/go-micro/events"
	"github.com/flylib/go-micro/registry"
	"github.com/flylib/go-micro/store"
	"github.com/flylib/go-micro/transport"
)

type Profile struct {
	Registry  registry.Registry
	Broker    broker.Broker
	Store     store.Store
	Transport transport.Transport
	Stream    events.Stream
}

// LocalProfile returns a profile with local mDNS as the registry, HTTP as the broker, file as the store, and HTTP as the transport
// It is used for local development and testing
func LocalProfile() (Profile, error) {
	stream, err := events.NewStream()
	return Profile{
		Registry:  registry.NewMDNSRegistry(),
		Broker:    broker.NewHttpBroker(),
		Store:     store.NewFileStore(),
		Transport: transport.NewHTTPTransport(),
		Stream:    stream,
	}, err
}

// Backend-specific profiles (e.g. NATS as registry/broker/store/transport)
// live with their plugin modules — import the plugins you need and compose
// a Profile in your own code.
