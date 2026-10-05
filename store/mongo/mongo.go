// Package mongo implements store.Store on MongoDB 4.4 or later.
//
// A store database is a MongoDB database and a table is a collection;
// characters MongoDB does not allow in those names are percent-encoded.
// Each record is one document:
//
//	{_id: <key>, value: <binary>, metadata: <JSON text>, expiresAt: <date>}
//
// Prefix and suffix scans are anchored regular expressions on _id, so a
// prefix scan uses the _id index; keys come back in byte order, as in the
// file and SQL stores. Expiry is computed and checked with the server
// clock ($$NOW), so client clock skew does not matter. A TTL index on
// expiresAt removes expired documents in the background; reads never
// return them, even before that runs.
package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

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

type clientKey struct{}

// WithClient makes the store use an existing client. The store does not
// disconnect it.
func WithClient(c *mongo.Client) store.Option {
	return func(o *store.Options) {
		if o.Context == nil {
			o.Context = context.Background()
		}
		o.Context = context.WithValue(o.Context, clientKey{}, c)
	}
}

// NewStore returns a MongoDB store. The first node is a connection string
// ("mongodb://user:pass@host:27017", "mongodb+srv://..."); none means
// mongodb://127.0.0.1:27017.
//
//	s := mongo.NewStore(store.Nodes("mongodb://127.0.0.1:27017"), store.Database("orders"))
func NewStore(opts ...store.Option) store.Store {
	s := &mongoStore{
		options: store.Options{Database: DefaultDatabase, Table: DefaultTable},
		ttl:     make(map[string]bool),
	}
	if err := s.Init(opts...); err != nil {
		logger.Logf(logger.ErrorLevel, "store/mongo: %v", err)
	}
	return s
}

type mongoStore struct {
	options store.Options
	client  *mongo.Client
	own     bool // client created from Nodes, disconnected by Close
	uri     string

	mu  sync.Mutex
	ttl map[string]bool // collections whose TTL index exists
}

func (s *mongoStore) Init(opts ...store.Option) error {
	for _, o := range opts {
		o(&s.options)
	}
	if s.options.Context != nil {
		if c, ok := s.options.Context.Value(clientKey{}).(*mongo.Client); ok && c != nil && c != s.client {
			s.disconnect()
			s.client, s.own, s.uri = c, false, ""
		}
	}
	uri := "mongodb://127.0.0.1:27017"
	if len(s.options.Nodes) > 0 {
		uri = s.options.Nodes[0]
	}
	if s.client == nil || (s.own && uri != s.uri) {
		c, err := mongo.Connect(options.Client().ApplyURI(uri))
		if err != nil {
			return err
		}
		s.disconnect()
		s.client, s.own, s.uri = c, true, uri
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return s.client.Ping(ctx, nil)
}

func (s *mongoStore) disconnect() {
	if s.own && s.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		_ = s.client.Disconnect(ctx)
	}
}

func (s *mongoStore) Options() store.Options { return s.options }

func (s *mongoStore) String() string { return "mongo" }

func (s *mongoStore) Close() error {
	s.disconnect()
	return nil
}

func (s *mongoStore) collection(database, table string) *mongo.Collection {
	if database == "" {
		database = s.options.Database
	}
	if database == "" {
		database = DefaultDatabase
	}
	if table == "" {
		table = s.options.Table
	}
	if table == "" {
		table = DefaultTable
	}
	return s.client.Database(escape(database, dbUnsafe)).Collection(escape(table, collUnsafe))
}

// Characters MongoDB refuses in database and collection names, plus the
// escape character itself.
const (
	dbUnsafe   = "/\\. \"$%"
	collUnsafe = "$%"
)

// escape percent-encodes unsafe bytes, so any store database or table
// name maps to a valid, distinct MongoDB name.
func escape(name, unsafe string) string {
	if !strings.ContainsAny(name, unsafe) && !strings.ContainsRune(name, 0) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == 0 || strings.IndexByte(unsafe, c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ensureTTL creates the TTL index of a collection once per process.
func (s *mongoStore) ensureTTL(ctx context.Context, c *mongo.Collection) error {
	name := c.Database().Name() + "." + c.Name()
	s.mu.Lock()
	done := s.ttl[name]
	s.mu.Unlock()
	if done {
		return nil
	}
	_, err := c.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "expiresAt", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return fmt.Errorf("store/mongo: create TTL index: %w", err)
	}
	s.mu.Lock()
	s.ttl[name] = true
	s.mu.Unlock()
	return nil
}

// live matches documents that have not expired, by the server clock.
var live = bson.D{{Key: "$or", Value: bson.A{
	bson.D{{Key: "expiresAt", Value: bson.D{{Key: "$exists", Value: false}}}},
	bson.D{{Key: "$expr", Value: bson.D{{Key: "$gt", Value: bson.A{"$expiresAt", "$$NOW"}}}}},
}}}

// keyFilter matches live documents whose _id has the prefix and suffix.
func keyFilter(prefix, suffix string) bson.D {
	conds := bson.A{live}
	if prefix != "" {
		conds = append(conds, bson.D{{Key: "_id", Value: bson.Regex{Pattern: "^" + regexp.QuoteMeta(prefix)}}})
	}
	if suffix != "" {
		conds = append(conds, bson.D{{Key: "_id", Value: bson.Regex{Pattern: regexp.QuoteMeta(suffix) + "$"}}})
	}
	return bson.D{{Key: "$and", Value: conds}}
}

type doc struct {
	Key       string `bson:"_id"`
	Value     []byte `bson:"value"`
	Metadata  string `bson:"metadata,omitempty"`
	Remaining *int64 `bson:"remaining,omitempty"` // ms until expiry, computed by the server
}

// projection returns the record fields and the time left before expiry.
var projection = bson.D{
	{Key: "value", Value: 1},
	{Key: "metadata", Value: 1},
	{Key: "remaining", Value: bson.D{{Key: "$subtract", Value: bson.A{"$expiresAt", "$$NOW"}}}},
}

func (d *doc) record() (*store.Record, error) {
	r := &store.Record{Key: d.Key, Value: d.Value, Metadata: map[string]interface{}{}}
	if r.Value == nil {
		r.Value = []byte{}
	}
	if d.Metadata != "" {
		if err := json.Unmarshal([]byte(d.Metadata), &r.Metadata); err != nil {
			return nil, err
		}
	}
	if d.Remaining != nil && *d.Remaining > 0 {
		r.Expiry = time.Duration(*d.Remaining) * time.Millisecond
	}
	return r, nil
}

func (s *mongoStore) Read(key string, opts ...store.ReadOption) ([]*store.Record, error) {
	var o store.ReadOptions
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	c := s.collection(o.Database, o.Table)

	if !o.Prefix && !o.Suffix {
		var d doc
		filter := bson.D{{Key: "$and", Value: bson.A{bson.D{{Key: "_id", Value: key}}, live}}}
		err := c.FindOne(ctx, filter, options.FindOne().SetProjection(projection)).Decode(&d)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, store.ErrNotFound
		}
		if err != nil {
			return nil, err
		}
		r, err := d.record()
		if err != nil {
			return nil, err
		}
		return []*store.Record{r}, nil
	}

	var prefix, suffix string
	if o.Prefix {
		prefix = key
	}
	if o.Suffix {
		suffix = key
	}
	fo := options.Find().SetProjection(projection).SetSort(bson.D{{Key: "_id", Value: 1}})
	page(fo, o.Limit, o.Offset)
	cur, err := c.Find(ctx, keyFilter(prefix, suffix), fo)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*store.Record
	for cur.Next(ctx) {
		var d doc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		r, err := d.record()
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, cur.Err()
}

func (s *mongoStore) List(opts ...store.ListOption) ([]string, error) {
	var o store.ListOptions
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	fo := options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).SetSort(bson.D{{Key: "_id", Value: 1}})
	page(fo, o.Limit, o.Offset)
	cur, err := s.collection(o.Database, o.Table).Find(ctx, keyFilter(o.Prefix, o.Suffix), fo)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var keys []string
	for cur.Next(ctx) {
		var d struct {
			Key string `bson:"_id"`
		}
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		keys = append(keys, d.Key)
	}
	return keys, cur.Err()
}

func page(fo *options.FindOptionsBuilder, limit, offset uint) {
	if limit > 0 {
		fo.SetLimit(int64(limit))
	}
	if offset > 0 {
		fo.SetSkip(int64(offset))
	}
}

func (s *mongoStore) Write(r *store.Record, opts ...store.WriteOption) error {
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
	if ttl < 0 {
		// already expired: as if written and then expired
		return s.Delete(r.Key, store.DeleteFrom(o.Database, o.Table))
	}

	value := r.Value
	if value == nil {
		value = []byte{}
	}
	set := bson.D{
		{Key: "value", Value: value},
		{Key: "metadata", Value: ""},
	}
	if len(r.Metadata) > 0 {
		b, err := json.Marshal(r.Metadata)
		if err != nil {
			return err
		}
		set[1].Value = string(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	c := s.collection(o.Database, o.Table)

	// an update pipeline, so the server clock sets expiresAt
	pipeline := bson.A{bson.D{{Key: "$replaceWith", Value: bson.D{{Key: "_id", Value: "$_id"}}}}, bson.D{{Key: "$set", Value: set}}}
	if ttl > 0 {
		if err := s.ensureTTL(ctx, c); err != nil {
			return err
		}
		ms := ttl.Milliseconds()
		if ms == 0 {
			ms = 1
		}
		pipeline = append(pipeline, bson.D{{Key: "$set", Value: bson.D{
			{Key: "expiresAt", Value: bson.D{{Key: "$add", Value: bson.A{"$$NOW", ms}}}},
		}}})
	}
	_, err := c.UpdateOne(ctx, bson.D{{Key: "_id", Value: r.Key}}, pipeline, options.UpdateOne().SetUpsert(true))
	return err
}

func (s *mongoStore) Delete(key string, opts ...store.DeleteOption) error {
	var o store.DeleteOptions
	for _, f := range opts {
		f(&o)
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	_, err := s.collection(o.Database, o.Table).DeleteOne(ctx, bson.D{{Key: "_id", Value: key}})
	return err
}
