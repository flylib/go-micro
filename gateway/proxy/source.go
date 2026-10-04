package proxy

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/flylib/go-micro/config/source"
	consulsrc "github.com/flylib/go-micro/config/source/consul"
	etcdsrc "github.com/flylib/go-micro/config/source/etcd"
	nacossrc "github.com/flylib/go-micro/config/source/nacos"
)

// RulesNone is the MICRO_GATEWAY_RULES value that runs without a rules
// document: convention routing, no plugins (SPEC 10).
const RulesNone = "none"

// SourceFromURI opens the rules source named by a MICRO_GATEWAY_RULES URI
// (SPEC 11). KV backends are go-micro config sources; files are polled.
func SourceFromURI(uri string) (source.Source, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("rules source: %w", err)
	}
	key := strings.TrimPrefix(u.Path, "/")
	switch u.Scheme {
	case "file":
		return newFileSource(u.Path), nil
	case "etcd":
		return etcdsrc.NewSource(etcdsrc.WithAddress(u.Host), etcdsrc.WithKey("/"+key), etcdsrc.WithFormat("yaml")), nil
	case "consul":
		return consulsrc.NewSource(consulsrc.WithAddress(u.Host), consulsrc.WithKey(key), consulsrc.WithFormat("yaml")), nil
	case "nacos":
		opts := []source.Option{nacossrc.WithAddress(u.Host), nacossrc.WithDataId(key), nacossrc.WithFormat("yaml")}
		if g := u.Query().Get("group"); g != "" {
			opts = append(opts, nacossrc.WithGroup(g))
		}
		if ns := u.Query().Get("namespace"); ns != "" {
			opts = append(opts, nacossrc.WithNamespaceId(ns))
		}
		return nacossrc.NewSource(opts...), nil
	}
	return nil, fmt.Errorf("rules source: unsupported scheme %q (want file, etcd, consul or nacos)", u.Scheme)
}

// filePoll is how often a file source is checked for changes.
const filePoll = time.Second

// fileSource polls a rules file by content. Polling, unlike file-event
// watching, survives atomic replacement by rename and symlink swaps
// (Kubernetes ConfigMaps), which drop an inotify/kqueue watch.
type fileSource struct{ path string }

func newFileSource(path string) source.Source { return fileSource{path: path} }

func (s fileSource) Read() (*source.ChangeSet, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	cs := &source.ChangeSet{Data: b, Format: "yaml", Source: s.String(), Timestamp: time.Now()}
	cs.Checksum = cs.Sum()
	return cs, nil
}

func (fileSource) Write(*source.ChangeSet) error { return errors.New("file rules source is read-only") }

func (s fileSource) Watch() (source.Watcher, error) {
	w := &fileWatcher{s: s, exit: make(chan struct{})}
	if b, err := os.ReadFile(s.path); err == nil {
		w.last = sha256.Sum256(b)
	}
	return w, nil
}

func (s fileSource) String() string { return "file" }

type fileWatcher struct {
	s    fileSource
	last [32]byte
	exit chan struct{}
}

func (w *fileWatcher) Next() (*source.ChangeSet, error) {
	t := time.NewTicker(filePoll)
	defer t.Stop()
	for {
		select {
		case <-w.exit:
			return nil, source.ErrWatcherStopped
		case <-t.C:
		}
		b, err := os.ReadFile(w.s.path)
		if err != nil {
			continue // mid-replace or briefly missing: try again next tick
		}
		if sum := sha256.Sum256(b); !bytes.Equal(sum[:], w.last[:]) {
			w.last = sum
			cs := &source.ChangeSet{Data: b, Format: "yaml", Source: w.s.String(), Timestamp: time.Now()}
			cs.Checksum = cs.Sum()
			return cs, nil
		}
	}
}

func (w *fileWatcher) Stop() error {
	select {
	case <-w.exit:
	default:
		close(w.exit)
	}
	return nil
}
