package store

import (
	"sort"
	"sync"
)

var (
	pluginsMu sync.RWMutex
	plugins   = map[string]func(...Option) Store{}
)

// RegisterPlugin makes an implementation selectable by name, for example
// with MICRO_STORE. Implementations call it from their package's init,
// so linking the package in (a blank import is enough) registers it.
func RegisterPlugin(name string, fn func(...Option) Store) {
	pluginsMu.Lock()
	plugins[name] = fn
	pluginsMu.Unlock()
}

// Plugin returns the constructor registered under name.
func Plugin(name string) (func(...Option) Store, bool) {
	pluginsMu.RLock()
	fn, ok := plugins[name]
	pluginsMu.RUnlock()
	return fn, ok
}

// Plugins returns the registered names, sorted.
func Plugins() []string {
	pluginsMu.RLock()
	out := make([]string, 0, len(plugins))
	for name := range plugins {
		out = append(out, name)
	}
	pluginsMu.RUnlock()
	sort.Strings(out)
	return out
}
