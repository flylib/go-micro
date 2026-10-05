// Package mongo provides a MongoDB model.Model implementation.
//
// Each registered table is a collection. The key field is stored as the
// document _id; other fields are stored under their column names (json tag
// or lowercased field name) with their BSON types, so numbers, booleans,
// times and nested values keep their types. Fields tagged model:"index"
// get an ascending index.
//
// Filters map to MongoDB operators: = $eq, != $ne, < $lt, > $gt, <= $lte,
// >= $gte, and LIKE to an anchored regular expression (% is any run of
// characters, _ any one character). Comparisons are typed, as in SQL: a
// number field does not equal the string "30".
package mongo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"github.com/flylib/go-micro/model"
)

// DefaultDatabase is used when the connection string names no database.
var DefaultDatabase = "micro"

const opTimeout = 10 * time.Second

type mongoModel struct {
	client *mongo.Client
	db     *mongo.Database
	own    bool // client created by New, disconnected by Close

	mu      sync.RWMutex
	schemas map[reflect.Type]*model.Schema
}

// New connects to MongoDB. The database comes from the connection string
// path ("mongodb://user:pass@host:27017/orders"), else DefaultDatabase.
// Connecting is lazy; Init pings the server.
func New(uri string) model.Model {
	cs, err := connstring.ParseAndValidate(uri)
	if err != nil {
		panic(fmt.Sprintf("model/mongo: bad connection string: %v", err))
	}
	c, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		panic(fmt.Sprintf("model/mongo: connect: %v", err))
	}
	db := cs.Database
	if db == "" {
		db = DefaultDatabase
	}
	m := NewFromClient(c, db).(*mongoModel)
	m.own = true
	return m
}

// NewFromClient uses an existing client and database name. Close does
// not disconnect a client it was given.
func NewFromClient(c *mongo.Client, database string) model.Model {
	return &mongoModel{
		client:  c,
		db:      c.Database(database),
		schemas: make(map[reflect.Type]*model.Schema),
	}
}

func (d *mongoModel) Init(opts ...model.Option) error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return d.client.Ping(ctx, nil)
}

func (d *mongoModel) Register(v interface{}, opts ...model.RegisterOption) error {
	schema := model.BuildSchema(v, opts...)
	if schema.Key == "" {
		return fmt.Errorf("model/mongo: %s has no key field (tag one model:\"key\" or name it id)", schema.Table)
	}
	d.mu.Lock()
	d.schemas[model.ResolveType(v)] = schema
	d.mu.Unlock()

	var idx []mongo.IndexModel
	for _, f := range schema.Fields {
		if f.Index && !f.IsKey {
			idx = append(idx, mongo.IndexModel{Keys: bson.D{{Key: f.Column, Value: 1}}})
		}
	}
	if len(idx) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if _, err := d.db.Collection(schema.Table).Indexes().CreateMany(ctx, idx); err != nil {
		return fmt.Errorf("model/mongo: create index: %w", err)
	}
	return nil
}

func (d *mongoModel) schema(v interface{}) (*model.Schema, error) {
	d.mu.RLock()
	s, ok := d.schemas[model.ResolveType(v)]
	d.mu.RUnlock()
	if !ok {
		return nil, model.ErrNotRegistered
	}
	return s, nil
}

func (d *mongoModel) Create(ctx context.Context, v interface{}) error {
	schema, err := d.schema(v)
	if err != nil {
		return err
	}
	_, err = d.db.Collection(schema.Table).InsertOne(ctx, toDoc(schema, v))
	if mongo.IsDuplicateKeyError(err) {
		return model.ErrDuplicateKey
	}
	if err != nil {
		return fmt.Errorf("model/mongo: create: %w", err)
	}
	return nil
}

func (d *mongoModel) Read(ctx context.Context, key string, v interface{}) error {
	schema, err := d.schema(v)
	if err != nil {
		return err
	}
	id, err := keyOf(schema, key)
	if err != nil {
		return err
	}
	raw, err := d.db.Collection(schema.Table).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return model.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("model/mongo: read: %w", err)
	}
	return fromDoc(schema, raw, reflect.ValueOf(v).Elem())
}

func (d *mongoModel) Update(ctx context.Context, v interface{}) error {
	schema, err := d.schema(v)
	if err != nil {
		return err
	}
	doc := toDoc(schema, v)
	res, err := d.db.Collection(schema.Table).ReplaceOne(ctx, bson.D{doc[0]}, doc)
	if err != nil {
		return fmt.Errorf("model/mongo: update: %w", err)
	}
	if res.MatchedCount == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (d *mongoModel) Delete(ctx context.Context, key string, v interface{}) error {
	schema, err := d.schema(v)
	if err != nil {
		return err
	}
	id, err := keyOf(schema, key)
	if err != nil {
		return err
	}
	res, err := d.db.Collection(schema.Table).DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	if err != nil {
		return fmt.Errorf("model/mongo: delete: %w", err)
	}
	if res.DeletedCount == 0 {
		return model.ErrNotFound
	}
	return nil
}

func (d *mongoModel) List(ctx context.Context, result interface{}, opts ...model.QueryOption) error {
	rv := reflect.ValueOf(result)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Slice {
		return fmt.Errorf("model/mongo: result must be a pointer to a slice")
	}
	sliceVal := rv.Elem()
	elemType := sliceVal.Type().Elem()
	structType := elemType
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}
	d.mu.RLock()
	schema, ok := d.schemas[structType]
	d.mu.RUnlock()
	if !ok {
		return model.ErrNotRegistered
	}

	q := model.ApplyQueryOptions(opts...)
	filter, err := Filter(schema, q.Filters)
	if err != nil {
		return err
	}
	fo := options.Find()
	if q.OrderBy != "" {
		dir := 1
		if q.Desc {
			dir = -1
		}
		sort := bson.D{{Key: column(schema, q.OrderBy), Value: dir}}
		if column(schema, q.OrderBy) != "_id" {
			sort = append(sort, bson.E{Key: "_id", Value: dir}) // stable pages
		}
		fo.SetSort(sort)
	}
	if q.Limit > 0 {
		fo.SetLimit(int64(q.Limit))
	}
	if q.Offset > 0 {
		fo.SetSkip(int64(q.Offset))
	}

	cur, err := d.db.Collection(schema.Table).Find(ctx, filter, fo)
	if err != nil {
		return fmt.Errorf("model/mongo: list: %w", err)
	}
	defer cur.Close(ctx)

	results := reflect.MakeSlice(sliceVal.Type(), 0, 0)
	for cur.Next(ctx) {
		vp := reflect.New(structType)
		if err := fromDoc(schema, cur.Current, vp.Elem()); err != nil {
			return err
		}
		if elemType.Kind() == reflect.Ptr {
			results = reflect.Append(results, vp)
		} else {
			results = reflect.Append(results, vp.Elem())
		}
	}
	if err := cur.Err(); err != nil {
		return fmt.Errorf("model/mongo: list: %w", err)
	}
	sliceVal.Set(results)
	return nil
}

func (d *mongoModel) Count(ctx context.Context, v interface{}, opts ...model.QueryOption) (int64, error) {
	schema, err := d.schema(v)
	if err != nil {
		return 0, err
	}
	filter, err := Filter(schema, model.ApplyQueryOptions(opts...).Filters)
	if err != nil {
		return 0, err
	}
	n, err := d.db.Collection(schema.Table).CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("model/mongo: count: %w", err)
	}
	return n, nil
}

func (d *mongoModel) Close() error {
	if !d.own {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return d.client.Disconnect(ctx)
}

func (d *mongoModel) String() string { return "mongo" }

// column maps a column name to its document field: the key is _id.
func column(schema *model.Schema, col string) string {
	if col == schema.Key {
		return "_id"
	}
	return col
}

// toDoc encodes a struct as a document with the key first, as _id.
func toDoc(schema *model.Schema, v interface{}) bson.D {
	fields := model.StructToMap(schema, v)
	doc := bson.D{{Key: "_id", Value: fields[schema.Key]}}
	for _, f := range schema.Fields {
		if !f.IsKey {
			doc = append(doc, bson.E{Key: f.Column, Value: fields[f.Column]})
		}
	}
	return doc
}

// fromDoc decodes each schema field from its document field into the
// struct, with the field's own Go type.
func fromDoc(schema *model.Schema, raw bson.Raw, sv reflect.Value) error {
	for _, f := range schema.Fields {
		val, err := raw.LookupErr(column(schema, f.Column))
		if err != nil {
			continue // absent: keep the zero value
		}
		fv := sv.FieldByName(f.Name)
		if !fv.IsValid() || !fv.CanSet() {
			continue
		}
		p := reflect.New(f.Type)
		if err := val.Unmarshal(p.Interface()); err != nil {
			return fmt.Errorf("model/mongo: decode %s: %w", f.Column, err)
		}
		fv.Set(p.Elem())
	}
	return nil
}

// keyOf converts a key passed as a string to the key field's type, so it
// matches the _id that Create stored.
func keyOf(schema *model.Schema, key string) (any, error) {
	for _, f := range schema.Fields {
		if !f.IsKey {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, err := strconv.ParseInt(key, 10, 64)
			if err != nil {
				return nil, model.ErrNotFound
			}
			return reflect.ValueOf(n).Convert(f.Type).Interface(), nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n, err := strconv.ParseUint(key, 10, 64)
			if err != nil {
				return nil, model.ErrNotFound
			}
			return reflect.ValueOf(n).Convert(f.Type).Interface(), nil
		}
	}
	return key, nil
}

var ops = map[string]string{"=": "$eq", "!=": "$ne", "<": "$lt", ">": "$gt", "<=": "$lte", ">=": "$gte"}

// Filter translates model filters into a MongoDB filter document.
func Filter(schema *model.Schema, filters []model.Filter) (bson.D, error) {
	var conds bson.A
	for _, f := range filters {
		field := column(schema, f.Field)
		var cond bson.D
		switch op := strings.ToUpper(f.Op); op {
		case "LIKE":
			cond = bson.D{{Key: field, Value: bson.Regex{Pattern: LikeToRegex(fmt.Sprint(f.Value))}}}
		default:
			mop, ok := ops[op]
			if !ok {
				return nil, fmt.Errorf("model/mongo: unsupported operator %q", f.Op)
			}
			cond = bson.D{{Key: field, Value: bson.D{{Key: mop, Value: f.Value}}}}
		}
		conds = append(conds, cond)
	}
	switch len(conds) {
	case 0:
		return bson.D{}, nil
	case 1:
		return conds[0].(bson.D), nil
	}
	return bson.D{{Key: "$and", Value: conds}}, nil
}

// LikeToRegex turns a SQL LIKE pattern into an anchored regular
// expression: % matches any run of characters, _ any one character.
func LikeToRegex(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return b.String()
}
