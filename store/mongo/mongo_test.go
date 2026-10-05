package mongo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/flylib/go-micro/store"
)

// newStore returns a store on MONGO_URI with its own database, or skips.
func newStore(t *testing.T, opts ...store.Option) store.Store {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set (e.g. mongodb://127.0.0.1:27017)")
	}
	db := fmt.Sprintf("micro_test_%d", time.Now().UnixNano())
	s := NewStore(append([]store.Option{store.Nodes(uri), store.Database(db)}, opts...)...)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := s.(*mongoStore).client
		names, _ := c.ListDatabaseNames(context.Background(), bson.D{{Key: "name", Value: bson.Regex{Pattern: "^" + db}}})
		for _, n := range names {
			_ = c.Database(n).Drop(context.Background())
		}
		_ = s.Close()
	})
	return s
}

func keys(recs []*store.Record) []string {
	out := []string{}
	for _, r := range recs {
		out = append(out, r.Key)
	}
	return out
}

func eq(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func TestReadWriteDelete(t *testing.T) {
	s := newStore(t)
	if _, err := s.Read("missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing key: want ErrNotFound, got %v", err)
	}
	val := []byte{0, 1, 2, 0xff, '\n'}
	if err := s.Write(&store.Record{Key: "a", Value: val, Metadata: map[string]interface{}{"owner": "bob", "n": 3}}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.Read("a")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "records", len(recs), 1)
	eq(t, "value", recs[0].Value, val)
	eq(t, "metadata", recs[0].Metadata, map[string]interface{}{"owner": "bob", "n": float64(3)})
	eq(t, "expiry", recs[0].Expiry, time.Duration(0))

	// overwrite replaces value and metadata
	if err := s.Write(&store.Record{Key: "a", Value: []byte{}}); err != nil {
		t.Fatal(err)
	}
	recs, err = s.Read("a")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "empty value", len(recs[0].Value), 0)
	eq(t, "metadata after overwrite", recs[0].Metadata, map[string]interface{}{})

	if err := s.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	ks, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "list after delete", len(ks), 0)
	if err := s.Delete("a"); err != nil {
		t.Fatalf("delete of a missing key: %v", err)
	}
}

func TestScans(t *testing.T) {
	s := newStore(t)
	for _, k := range []string{"user/2", "user/10", "user/1", "order/1", "user/1/x", "users"} {
		if err := s.Write(&store.Record{Key: k, Value: []byte(k)}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(opts ...store.ListOption) []string {
		t.Helper()
		ks, err := s.List(opts...)
		if err != nil {
			t.Fatal(err)
		}
		if ks == nil {
			ks = []string{}
		}
		return ks
	}
	read := func(key string, opts ...store.ReadOption) []string {
		t.Helper()
		recs, err := s.Read(key, opts...)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recs {
			if string(r.Value) != r.Key {
				t.Fatalf("record %s has value %q", r.Key, r.Value)
			}
		}
		return keys(recs)
	}

	eq(t, "list", list(), []string{"order/1", "user/1", "user/1/x", "user/10", "user/2", "users"})
	eq(t, "list prefix", list(store.ListPrefix("user/")), []string{"user/1", "user/1/x", "user/10", "user/2"})
	eq(t, "list suffix", list(store.ListSuffix("/1")), []string{"order/1", "user/1"})
	eq(t, "list prefix+suffix", list(store.ListPrefix("user/"), store.ListSuffix("1")), []string{"user/1"})
	eq(t, "list limit", list(store.ListLimit(2)), []string{"order/1", "user/1"})
	eq(t, "list offset", list(store.ListOffset(4)), []string{"user/2", "users"})
	eq(t, "list page", list(store.ListPrefix("user/"), store.ListLimit(2), store.ListOffset(1)), []string{"user/1/x", "user/10"})
	eq(t, "list past the end", list(store.ListOffset(10)), []string{})

	eq(t, "read prefix", read("user/1", store.ReadPrefix()), []string{"user/1", "user/1/x", "user/10"})
	eq(t, "read suffix", read("/1", store.ReadSuffix()), []string{"order/1", "user/1"})
	eq(t, "read prefix page", read("user", store.ReadPrefix(), store.ReadLimit(2), store.ReadOffset(2)), []string{"user/10", "user/2"})
	eq(t, "read all", len(read("", store.ReadPrefix())), 6)
	eq(t, "read no match", read("nope", store.ReadPrefix()), []string{})

	// regex metacharacters in keys are literal
	if err := s.Write(&store.Record{Key: "a.b*c", Value: []byte("a.b*c")}); err != nil {
		t.Fatal(err)
	}
	eq(t, "literal prefix", list(store.ListPrefix("a.b*")), []string{"a.b*c"})
	eq(t, "literal prefix no match", list(store.ListPrefix("a.")), []string{"a.b*c"})
	eq(t, "dot is not a wildcard", list(store.ListPrefix("axb")), []string{})
}

func TestExpiry(t *testing.T) {
	s := newStore(t)
	if err := s.Write(&store.Record{Key: "short", Value: []byte("x")}, store.WriteTTL(300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(&store.Record{Key: "record-expiry", Value: []byte("x"), Expiry: 300 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	// TTL wins over Expiry
	if err := s.Write(&store.Record{Key: "both", Value: []byte("x")},
		store.WriteExpiry(time.Now().Add(time.Hour)), store.WriteTTL(300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(&store.Record{Key: "long", Value: []byte("x")}, store.WriteExpiry(time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	// rewritten without expiry: keeps no TTL
	if err := s.Write(&store.Record{Key: "persist", Value: []byte("x")}, store.WriteTTL(300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(&store.Record{Key: "persist", Value: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	recs, err := s.Read("short")
	if err != nil {
		t.Fatal(err)
	}
	if e := recs[0].Expiry; e <= 0 || e > 300*time.Millisecond {
		t.Fatalf("remaining expiry %v", e)
	}

	time.Sleep(600 * time.Millisecond)
	for _, k := range []string{"short", "record-expiry", "both"} {
		if _, err := s.Read(k); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s after expiry: %v", k, err)
		}
	}
	ks, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "live keys", ks, []string{"long", "persist"})
	recs, err = s.Read("long")
	if err != nil {
		t.Fatal(err)
	}
	if e := recs[0].Expiry; e < 59*time.Minute {
		t.Fatalf("long expiry %v", e)
	}

	// an expiry already in the past removes the record
	if err := s.Write(&store.Record{Key: "long", Value: []byte("x")}, store.WriteExpiry(time.Now().Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("long"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("written already expired: %v", err)
	}
}

func TestTables(t *testing.T) {
	s := newStore(t, store.Table("orders"))
	db := s.Options().Database
	if err := s.Write(&store.Record{Key: "k", Value: []byte("orders")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(&store.Record{Key: "k", Value: []byte("users")}, store.WriteTo(db, "users")); err != nil {
		t.Fatal(err)
	}
	recs, err := s.Read("k")
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "default table", string(recs[0].Value), "orders")
	recs, err = s.Read("k", store.ReadFrom(db, "users"))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "other table", string(recs[0].Value), "users")

	// a scoped handle sees only its table
	users := store.Scope(s, db, "users")
	ks, err := users.List()
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "scoped list", ks, []string{"k"})
	if err := users.Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("k"); err != nil {
		t.Fatalf("delete in one table removed another's key: %v", err)
	}

	// names are escaped: "a/b"+"c" is not "a"+"b/c"
	if err := s.Write(&store.Record{Key: "x", Value: []byte("1")}, store.WriteTo(db+"/a", "c")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("x", store.ReadFrom(db, "a/c")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("database/table names collide: %v", err)
	}
}

func TestWithClient(t *testing.T) {
	s := newStore(t)
	c := s.(*mongoStore).client
	shared := NewStore(WithClient(c), store.Database(s.Options().Database))
	if err := s.Write(&store.Record{Key: "k", Value: []byte("v")}); err != nil {
		t.Fatal(err)
	}
	if _, err := shared.Read("k"); err != nil {
		t.Fatal(err)
	}
	// closing a store must not disconnect a client it was given
	if err := shared.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(context.Background(), nil); err != nil {
		t.Fatalf("client closed by a store that did not own it: %v", err)
	}
}

func TestTTLIndex(t *testing.T) {
	s := newStore(t)
	if err := s.Write(&store.Record{Key: "k", Value: []byte("v")}, store.WriteTTL(time.Hour)); err != nil {
		t.Fatal(err)
	}
	c := s.(*mongoStore).collection("", "")
	cur, err := c.Indexes().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var idx []bson.M
	if err := cur.All(context.Background(), &idx); err != nil {
		t.Fatal(err)
	}
	for _, ix := range idx {
		if ix["name"] == "expiresAt_1" && ix["expireAfterSeconds"] != nil {
			return
		}
	}
	t.Fatalf("no TTL index: %v", idx)
}

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"orders":      "orders",
		"service/foo": "service%2Ffoo",
		"a.b c$d%":    "a%2Eb%20c%24d%25",
	} {
		if got := escape(in, dbUnsafe); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
	if got := escape("service/foo", collUnsafe); got != "service/foo" {
		t.Errorf("collection names keep slashes: %q", got)
	}
}
