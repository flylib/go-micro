// Package redis implements store.Store on Redis and Redis-compatible
// servers (Dragonfly, Valkey), standalone or Cluster.
//
// Layout, per database and table (the hash tag keeps a table in one
// Cluster slot, so its scripts and transactions are single-slot):
//
//	micro:store:{<db>/<table>}:r:<key>   hash: v = value, m = metadata JSON
//	micro:store:{<db>/<table>}:idx       zset, score 0: keys in byte order
//	micro:store:{<db>/<table>}:exp       zset: key -> expiry (server ms)
//
// Records expire with PEXPIRE. Scans (List, prefix and suffix reads) walk
// the idx set in byte order, as the file and SQL stores return keys, after
// a script drops the keys whose expiry has passed. Scripts only touch the
// keys they declare and read the clock with TIME, so they run on
// Dragonfly's default script mode and do not depend on client clocks.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/flylib/go-micro/logger"
	"github.com/flylib/go-micro/store"
)

var (
	// DefaultDatabase and DefaultTable apply when neither the store nor
	// the call names one, as in the memory store.
	DefaultDatabase = "micro"
	DefaultTable    = "micro"
)

const opTimeout = 10 * time.Second

// scanPage is how many index entries a suffix scan reads per round trip;
// a variable so tests can page through small tables.
var scanPage int64 = 1000

var (
	// write replaces a record and indexes it.
	// KEYS: record, idx, exp. ARGV: key, value, metadata ("" for none), ttl ms (0 for none).
	write = redis.NewScript(`
redis.call("DEL", KEYS[1])
if ARGV[3] ~= "" then
	redis.call("HSET", KEYS[1], "v", ARGV[2], "m", ARGV[3])
else
	redis.call("HSET", KEYS[1], "v", ARGV[2])
end
redis.call("ZADD", KEYS[2], 0, ARGV[1])
local ttl = tonumber(ARGV[4])
if ttl > 0 then
	redis.call("PEXPIRE", KEYS[1], ttl)
	local t = redis.call("TIME")
	redis.call("ZADD", KEYS[3], tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000) + ttl, ARGV[1])
else
	redis.call("ZREM", KEYS[3], ARGV[1])
end
return 1`)

	// purge drops expired keys from the index. KEYS: idx, exp.
	purge = redis.NewScript(`
local t = redis.call("TIME")
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local gone = redis.call("ZRANGEBYSCORE", KEYS[2], "-inf", "(" .. now)
for i = 1, #gone, 500 do
	local j = math.min(i + 499, #gone)
	redis.call("ZREM", KEYS[1], unpack(gone, i, j))
	redis.call("ZREM", KEYS[2], unpack(gone, i, j))
end
return #gone`)
)

type clientKey struct{}

// WithClient makes the store use an existing client, for options that
// Nodes cannot express (Sentinel, TLS config, pool sizes). The store does
// not close it.
func WithClient(c redis.UniversalClient) store.Option {
	return func(o *store.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, clientKey{}, c)
	}
}

// NewStore returns a Redis store. Nodes are "host:port" addresses or
// redis:// / rediss:// URLs (with password and database number); several
// nodes make a Cluster client; none means 127.0.0.1:6379.
//
//	s := redis.NewStore(store.Nodes("redis://:secret@127.0.0.1:6379/0"), store.Table("orders"))
func NewStore(opts ...store.Option) store.Store {
	s := &redisStore{options: store.Options{Database: DefaultDatabase, Table: DefaultTable}}
	if err := s.Init(opts...); err != nil {
		logger.Logf(logger.ErrorLevel, "store/redis: %v", err)
	}
	return s
}

type redisStore struct {
	options store.Options
	client  redis.UniversalClient
	own     bool     // client created from Nodes, closed by Close
	nodes   []string // Nodes the own client was built from
}

func (s *redisStore) Init(opts ...store.Option) error {
	for _, o := range opts {
		o(&s.options)
	}
	if s.options.Context != nil {
		if c, ok := s.options.Context.Value(clientKey{}).(redis.UniversalClient); ok && c != nil {
			if s.own && s.client != nil && s.client != c {
				_ = s.client.Close()
			}
			s.client, s.own, s.nodes = c, false, nil
		}
	}
	if s.client == nil || (s.own && !equal(s.nodes, s.options.Nodes)) {
		c, err := NewClient(s.options.Nodes...)
		if err != nil {
			return err
		}
		if s.own && s.client != nil {
			_ = s.client.Close()
		}
		s.client, s.own = c, true
		s.nodes = append([]string(nil), s.options.Nodes...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return s.client.Ping(ctx).Err()
}

// NewClient builds a client from node addresses: none means
// 127.0.0.1:6379, one is an address or a redis:// URL, several make a
// Cluster client.
func NewClient(nodes ...string) (redis.UniversalClient, error) {
	switch len(nodes) {
	case 0:
		return redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"}), nil
	case 1:
		if strings.HasPrefix(nodes[0], "redis://") || strings.HasPrefix(nodes[0], "rediss://") {
			o, err := redis.ParseURL(nodes[0])
			if err != nil {
				return nil, err
			}
			return redis.NewClient(o), nil
		}
		return redis.NewClient(&redis.Options{Addr: nodes[0]}), nil
	}
	return redis.NewClusterClient(&redis.ClusterOptions{Addrs: nodes}), nil
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *redisStore) Options() store.Options { return s.options }

func (s *redisStore) String() string { return "redis" }

func (s *redisStore) Close() error {
	if s.own && s.client != nil {
		return s.client.Close()
	}
	return nil
}

// table holds the keys of one database/table.
type table struct {
	base string
}

func (s *redisStore) table(database, tbl string) table {
	if database == "" {
		database = s.options.Database
	}
	if database == "" {
		database = DefaultDatabase
	}
	if tbl == "" {
		tbl = s.options.Table
	}
	if tbl == "" {
		tbl = DefaultTable
	}
	// escaping keeps "a/b"+"c" and "a"+"b/c" apart and braces out of the tag
	return table{base: "micro:store:{" + url.QueryEscape(database) + "/" + url.QueryEscape(tbl) + "}"}
}

func (t table) record(key string) string { return t.base + ":r:" + key }
func (t table) idx() string              { return t.base + ":idx" }
func (t table) exp() string              { return t.base + ":exp" }

func (s *redisStore) Read(key string, opts ...store.ReadOption) ([]*store.Record, error) {
	var o store.ReadOptions
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	t := s.table(o.Database, o.Table)

	if !o.Prefix && !o.Suffix {
		recs, err := s.fetch(ctx, t, []string{key})
		if err != nil {
			return nil, err
		}
		if len(recs) == 0 {
			return nil, store.ErrNotFound
		}
		return recs, nil
	}
	var prefix, suffix string
	if o.Prefix {
		prefix = key
	}
	if o.Suffix {
		suffix = key
	}
	keys, err := s.scan(ctx, t, prefix, suffix, o.Limit, o.Offset)
	if err != nil {
		return nil, err
	}
	return s.fetch(ctx, t, keys)
}

func (s *redisStore) List(opts ...store.ListOption) ([]string, error) {
	var o store.ListOptions
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return s.scan(ctx, s.table(o.Database, o.Table), o.Prefix, o.Suffix, o.Limit, o.Offset)
}

func (s *redisStore) Write(r *store.Record, opts ...store.WriteOption) error {
	var o store.WriteOptions
	for _, f := range opts {
		f(&o)
	}
	ttl := r.Expiry
	if !o.Expiry.IsZero() {
		ttl = time.Until(o.Expiry)
		if ttl == 0 {
			ttl = -1
		}
	}
	if o.TTL != 0 {
		ttl = o.TTL
	}
	t := s.table(o.Database, o.Table)
	if ttl < 0 {
		// already expired: as if written and then expired
		return s.Delete(r.Key, store.DeleteFrom(o.Database, o.Table))
	}
	ms := ttl.Milliseconds()
	if ttl > 0 && ms == 0 {
		ms = 1
	}
	var meta string
	if len(r.Metadata) > 0 {
		b, err := json.Marshal(r.Metadata)
		if err != nil {
			return err
		}
		meta = string(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return write.Run(ctx, s.client, []string{t.record(r.Key), t.idx(), t.exp()}, r.Key, r.Value, meta, ms).Err()
}

func (s *redisStore) Delete(key string, opts ...store.DeleteOption) error {
	var o store.DeleteOptions
	for _, f := range opts {
		f(&o)
	}
	t := s.table(o.Database, o.Table)
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	_, err := s.client.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Del(ctx, t.record(key))
		p.ZRem(ctx, t.idx(), key)
		p.ZRem(ctx, t.exp(), key)
		return nil
	})
	return err
}

// scan returns the live keys of a table that match prefix and suffix, in
// byte order, after offset, at most limit (0: all).
func (s *redisStore) scan(ctx context.Context, t table, prefix, suffix string, limit, offset uint) ([]string, error) {
	if err := purge.Run(ctx, s.client, []string{t.idx(), t.exp()}).Err(); err != nil {
		return nil, err
	}
	min, max := "-", "+"
	if prefix != "" {
		min = "[" + prefix
		if end, ok := prefixEnd(prefix); ok {
			max = "(" + end
		}
	}
	if suffix == "" {
		// the server pages for us
		by := &redis.ZRangeBy{Min: min, Max: max}
		if limit > 0 || offset > 0 {
			by.Offset, by.Count = int64(offset), int64(limit)
			if limit == 0 {
				by.Count = -1
			}
		}
		return s.client.ZRangeByLex(ctx, t.idx(), by).Result()
	}
	var out []string
	skip := offset
	for page := int64(0); ; page += scanPage {
		keys, err := s.client.ZRangeByLex(ctx, t.idx(), &redis.ZRangeBy{Min: min, Max: max, Offset: page, Count: scanPage}).Result()
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			if !strings.HasSuffix(k, suffix) {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			out = append(out, k)
			if limit > 0 && uint(len(out)) == limit {
				return out, nil
			}
		}
		if int64(len(keys)) < scanPage {
			return out, nil
		}
	}
}

// prefixEnd returns the smallest string greater than every string with
// the prefix, or false when there is none (the prefix is all 0xff).
func prefixEnd(p string) (string, bool) {
	b := []byte(p)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < 0xff {
			b[i]++
			return string(b[:i+1]), true
		}
	}
	return "", false
}

// fetch reads records in the order of keys, skipping keys that no longer
// exist (deleted or expired since they were listed).
func (s *redisStore) fetch(ctx context.Context, t table, keys []string) ([]*store.Record, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	type pending struct {
		fields *redis.MapStringStringCmd
		ttl    *redis.DurationCmd
	}
	cmds := make([]pending, len(keys))
	_, err := s.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = pending{p.HGetAll(ctx, t.record(k)), p.PTTL(ctx, t.record(k))}
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	out := make([]*store.Record, 0, len(keys))
	for i, k := range keys {
		fields, err := cmds[i].fields.Result()
		if err != nil {
			return nil, err
		}
		v, ok := fields["v"]
		if !ok {
			continue
		}
		r := &store.Record{Key: k, Value: []byte(v), Metadata: map[string]interface{}{}}
		if m := fields["m"]; m != "" {
			if err := json.Unmarshal([]byte(m), &r.Metadata); err != nil {
				return nil, err
			}
		}
		if d, err := cmds[i].ttl.Result(); err == nil && d > 0 {
			r.Expiry = d
		}
		out = append(out, r)
	}
	return out, nil
}
