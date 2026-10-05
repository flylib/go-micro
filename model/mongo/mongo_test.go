package mongo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/flylib/go-micro/model"
)

type User struct {
	ID      string    `json:"id" model:"key"`
	Name    string    `json:"name" model:"index"`
	Age     int       `json:"age"`
	Score   float64   `json:"score"`
	Active  bool      `json:"active"`
	Tags    []string  `json:"tags"`
	Created time.Time `json:"created"`
}

type Counter struct {
	ID    int64 `json:"id"`
	Value int   `json:"value"`
}

func TestLikeToRegex(t *testing.T) {
	for _, tc := range []struct {
		like       string
		match, not []string
	}{
		{"al%", []string{"al", "alice"}, []string{"bal", "Al"}},
		{"%ce", []string{"alice", "ce"}, []string{"cel"}},
		{"%li%", []string{"alice", "li"}, []string{"lx"}},
		{"a_c", []string{"abc"}, []string{"ac", "abbc"}},
		{"a.b(c)", []string{"a.b(c)"}, []string{"axb(c)"}},
	} {
		re := regexp.MustCompile(LikeToRegex(tc.like))
		for _, s := range tc.match {
			if !re.MatchString(s) {
				t.Errorf("%q should match %q", tc.like, s)
			}
		}
		for _, s := range tc.not {
			if re.MatchString(s) {
				t.Errorf("%q should not match %q", tc.like, s)
			}
		}
	}
}

func TestFilter(t *testing.T) {
	schema := model.BuildSchema(User{})
	f, err := Filter(schema, []model.Filter{{Field: "id", Op: "=", Value: "u1"}})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.D{{Key: "_id", Value: bson.D{{Key: "$eq", Value: "u1"}}}}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("key filter: %v", f)
	}
	f, err = Filter(schema, []model.Filter{
		{Field: "age", Op: ">=", Value: 18},
		{Field: "age", Op: "<", Value: 65},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f) != 1 || f[0].Key != "$and" || len(f[0].Value.(bson.A)) != 2 {
		t.Fatalf("two filters on one field must not overwrite each other: %v", f)
	}
	if _, err := Filter(schema, []model.Filter{{Field: "age", Op: "~", Value: 1}}); err == nil {
		t.Fatal("unknown operator accepted")
	}
}

// newModel returns a model on MONGO_URI in a fresh database, or skips.
func newModel(t *testing.T) (model.Model, *mongo.Database) {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set (e.g. mongodb://127.0.0.1:27017)")
	}
	c, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := c.Database(fmt.Sprintf("micro_test_%d", time.Now().UnixNano()))
	t.Cleanup(func() {
		_ = db.Drop(context.Background())
		_ = c.Disconnect(context.Background())
	})
	m := NewFromClient(c, db.Name())
	if err := m.Init(); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(&User{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Register(&Counter{}); err != nil {
		t.Fatal(err)
	}
	return m, db
}

func TestCRUD(t *testing.T) {
	m, db := newModel(t)
	ctx := context.Background()
	created := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	u := &User{ID: "u1", Name: "Alice", Age: 30, Score: 9.5, Active: true, Tags: []string{"a", "b"}, Created: created}
	if err := m.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := m.Create(ctx, u); !errors.Is(err, model.ErrDuplicateKey) {
		t.Fatalf("duplicate: %v", err)
	}

	var got User
	if err := m.Read(ctx, "u1", &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, u) {
		t.Fatalf("read back %+v, want %+v", got, *u)
	}
	// stored with the key as _id and BSON types
	raw, err := db.Collection("users").FindOne(ctx, bson.D{{Key: "_id", Value: "u1"}}).Raw()
	if err != nil {
		t.Fatal(err)
	}
	if at := raw.Lookup("age").Type; (at != bson.TypeInt32 && at != bson.TypeInt64) || raw.Lookup("created").Type != bson.TypeDateTime {
		t.Fatalf("stored types: age %v, created %v", raw.Lookup("age").Type, raw.Lookup("created").Type)
	}
	if _, err := raw.LookupErr("id"); err == nil {
		t.Fatal("key stored twice")
	}

	u.Age = 31
	u.Tags = nil
	if err := m.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	got = User{}
	if err := m.Read(ctx, "u1", &got); err != nil {
		t.Fatal(err)
	}
	if got.Age != 31 || got.Tags != nil {
		t.Fatalf("after update: %+v", got)
	}
	if err := m.Update(ctx, &User{ID: "nobody"}); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}

	if err := m.Delete(ctx, "u1", &User{}); err != nil {
		t.Fatal(err)
	}
	if err := m.Read(ctx, "u1", &got); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("read deleted: %v", err)
	}
	if err := m.Delete(ctx, "u1", &User{}); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}

	type Unregistered struct{ ID string }
	if err := m.Create(ctx, &Unregistered{ID: "x"}); !errors.Is(err, model.ErrNotRegistered) {
		t.Fatalf("unregistered: %v", err)
	}
}

func TestIntegerKey(t *testing.T) {
	m, _ := newModel(t)
	ctx := context.Background()
	if err := m.Create(ctx, &Counter{ID: 42, Value: 1}); err != nil {
		t.Fatal(err)
	}
	var c Counter
	if err := m.Read(ctx, "42", &c); err != nil || c.Value != 1 {
		t.Fatalf("read by integer key: %+v %v", c, err)
	}
	if err := m.Read(ctx, "forty-two", &c); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("non-numeric key: %v", err)
	}
	if err := m.Delete(ctx, "42", &Counter{}); err != nil {
		t.Fatal(err)
	}
}

func TestListAndCount(t *testing.T) {
	m, db := newModel(t)
	ctx := context.Background()
	for i, name := range []string{"alice", "bob", "carol", "dave", "alfred"} {
		u := &User{ID: fmt.Sprintf("u%d", i), Name: name, Age: 20 + i*10, Active: i%2 == 0}
		if err := m.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	names := func(opts ...model.QueryOption) []string {
		t.Helper()
		var us []*User
		if err := m.List(ctx, &us, opts...); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, u := range us {
			out = append(out, u.Name)
		}
		return out
	}
	eq := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %v, want %v", what, got, want)
		}
	}
	eq("order", names(model.OrderAsc("name")), []string{"alfred", "alice", "bob", "carol", "dave"})
	eq("order desc + page", names(model.OrderDesc("age"), model.Limit(2), model.Offset(1)), []string{"dave", "carol"})
	eq("where", names(model.Where("active", true), model.OrderAsc("name")), []string{"alfred", "alice", "carol"})
	eq("range", names(model.WhereOp("age", ">=", 30), model.WhereOp("age", "<", 50), model.OrderAsc("age")), []string{"bob", "carol"})
	eq("like", names(model.WhereOp("name", "LIKE", "al%"), model.OrderAsc("name")), []string{"alfred", "alice"})
	eq("not equal", names(model.WhereOp("name", "!=", "bob"), model.OrderAsc("id")), []string{"alice", "carol", "dave", "alfred"})
	eq("by key", names(model.Where("id", "u2")), []string{"carol"})

	var vals []User // slice of values works too
	if err := m.List(ctx, &vals, model.Limit(1), model.OrderAsc("name")); err != nil || len(vals) != 1 || vals[0].Name != "alfred" {
		t.Fatalf("value slice: %v %v", vals, err)
	}

	n, err := m.Count(ctx, &User{}, model.WhereOp("age", ">", 30))
	if err != nil || n != 3 {
		t.Fatalf("count: %d %v", n, err)
	}
	if n, _ := m.Count(ctx, &User{}); n != 5 {
		t.Fatalf("count all: %d", n)
	}
	var bad []*User
	if err := m.List(ctx, &bad, model.WhereOp("age", "~", 1)); err == nil {
		t.Fatal("unsupported operator accepted")
	}

	// model:"index" created an index on name
	cur, err := db.Collection("users").Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var idx []bson.M
	if err := cur.All(ctx, &idx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ix := range idx {
		if ix["name"] == "name_1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no index on name: %v", idx)
	}
}
