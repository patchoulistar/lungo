package lungo

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/256dpi/lungo/bsonkit"
	"github.com/256dpi/lungo/mongokit"
)

var _ ICollection = &Collection{}

// Collection wraps an Engine to be mongo compatible.
type Collection struct {
	engine *Engine
	handle Handle
}

// Aggregate implements the ICollection.Aggregate method.
func (c *Collection) Aggregate(context.Context, interface{}, ...*options.AggregateOptions) (ICursor, error) {
	panic("lungo: not implemented")
}

// BulkWrite implements the ICollection.BulkWrite method.
func (c *Collection) BulkWrite(ctx context.Context, models []mongo.WriteModel, opts ...*options.BulkWriteOptions) (*mongo.BulkWriteResult, error) {
	// merge options
	opt := options.MergeBulkWriteOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"Ordered": supported,
	})

	// get ordered
	var ordered bool
	if opt.Ordered != nil {
		ordered = *opt.Ordered
	}

	// prepare operations
	ops := make([]Operation, 0, len(models))

	// transform models
	for _, item := range models {
		// prepare variables
		var opcode Opcode
		var document interface{}
		var filter interface{}
		var upsert *bool
		var limit int
		var arrayFilters []interface{}

		// set variables
		switch model := item.(type) {
		case *mongo.InsertOneModel:
			opcode = Insert
			document = model.Document
			limit = 1
		case *mongo.ReplaceOneModel:
			opcode = Replace
			filter = model.Filter
			document = model.Replacement
			upsert = model.Upsert
			limit = 1
		case *mongo.UpdateOneModel:
			opcode = Update
			filter = model.Filter
			document = model.Update
			upsert = model.Upsert
			limit = 1
			if model.ArrayFilters != nil {
				arrayFilters = model.ArrayFilters.Filters
			}
		case *mongo.UpdateManyModel:
			opcode = Update
			filter = model.Filter
			document = model.Update
			upsert = model.Upsert
			limit = 0
			if model.ArrayFilters != nil {
				arrayFilters = model.ArrayFilters.Filters
			}
		case *mongo.DeleteOneModel:
			opcode = Delete
			filter = model.Filter
			limit = 1
		case *mongo.DeleteManyModel:
			opcode = Delete
			filter = model.Filter
			limit = 0
		}

		// prepare operation
		op := Operation{
			Opcode: opcode,
			Limit:  limit,
		}

		// transform document
		if document != nil {
			doc, err := bsonkit.Transform(document)
			if err != nil {
				return nil, err
			}
			op.Document = doc
		}

		// transform filter
		if filter != nil {
			flt, err := bsonkit.Transform(filter)
			if err != nil {
				return nil, err
			}
			op.Filter = flt
		}

		// check upsert
		if upsert != nil {
			op.Upsert = *upsert
		}

		// transform array filters
		if arrayFilters != nil {
			arrFlt, err := bsonkit.TransformList(arrayFilters)
			if err != nil {
				return nil, err
			}
			op.ArrayFilters = arrFlt
		}

		// add operation
		ops = append(ops, op)
	}

	// run bulk
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Bulk(c.handle, ops, ordered)
	})
	if err != nil {
		return nil, err
	}

	// get results
	results := res.([]Result)

	// prepare result
	result := &mongo.BulkWriteResult{
		InsertedCount: 0,
		MatchedCount:  0,
		ModifiedCount: 0,
		DeletedCount:  0,
		UpsertedCount: 0,
		UpsertedIDs:   map[int64]interface{}{},
	}

	// prepare errors
	errors := make(mongo.WriteErrors, 0, len(results))

	// apply bulk results
	for i, res := range results {
		// check error
		if res.Error != nil {
			errors = append(errors, mongo.WriteError{
				Index:   i,
				Code:    0,
				Message: res.Error.Error(),
			})
			continue
		}

		// collect results
		switch ops[i].Opcode {
		case Insert:
			result.InsertedCount += int64(len(res.Modified))
		case Replace:
			result.MatchedCount += int64(len(res.Matched))
			result.ModifiedCount += int64(len(res.Modified))
			if res.Upserted != nil {
				result.UpsertedCount++
				result.UpsertedIDs[int64(i)] = bsonkit.Get(res.Upserted, "_id")
			}
		case Update:
			result.MatchedCount += int64(len(res.Matched))
			result.ModifiedCount += int64(len(res.Modified))
			if res.Upserted != nil {
				result.UpsertedCount++
				result.UpsertedIDs[int64(i)] = bsonkit.Get(res.Upserted, "_id")
			}
		case Delete:
			result.DeletedCount += int64(len(res.Matched))
		}
	}

	// prepare error
	err = nil
	if len(errors) > 0 {
		err = errors
	}

	return result, err
}

// Clone implements the ICollection.Clone method.
func (c *Collection) Clone(opts ...*options.CollectionOptions) (ICollection, error) {
	// merge options
	opt := options.MergeCollectionOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"ReadConcern":    ignored,
		"WriteConcern":   ignored,
		"ReadPreference": ignored,
	})

	return &Collection{
		engine: c.engine,
		handle: c.handle,
	}, nil
}

// CountDocuments implements the ICollection.CountDocuments method.
func (c *Collection) CountDocuments(ctx context.Context, filter interface{}, opts ...*options.CountOptions) (int64, error) {
	// merge options
	opt := options.MergeCountOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"Limit":   supported,
		"MaxTime": ignored,
		"Skip":    supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return 0, err
	}

	// get skip
	var skip int
	if opt.Skip != nil {
		skip = int(*opt.Skip)
	}

	// get limit
	var limit int
	if opt.Limit != nil {
		limit = int(*opt.Limit)
	}

	// find documents
	res, err := useTransaction(ctx, c.engine, false, func(txn *Transaction) (interface{}, error) {
		return txn.Find(c.handle, query, nil, skip, limit)
	})
	if err != nil {
		return 0, err
	}

	// get list
	list := res.(*Result).Matched

	return int64(len(list)), nil
}

// Database implements the ICollection.Database method.
func (c *Collection) Database() IDatabase {
	return &Database{
		name:   c.handle[0],
		engine: c.engine,
	}
}

// DeleteMany implements the ICollection.DeleteMany method.
func (c *Collection) DeleteMany(ctx context.Context, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {

	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}

	// merge options
	opt := options.MergeDeleteOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// delete documents
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Delete(c.handle, query, nil, 0, 0)
	})
	if err != nil {
		return nil, err
	}

	// get list
	list := res.(*Result).Matched

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.DeleteResult{
		DeletedCount: int64(len(list)),
	}, nil
}

// DeleteOne implements the ICollection.DeleteOne method.
func (c *Collection) DeleteOne(ctx context.Context, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}

	// merge options
	opt := options.MergeDeleteOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// delete document
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Delete(c.handle, query, nil, 0, 1)
	})
	if err != nil {
		return nil, err
	}

	// get list
	list := res.(*Result).Matched

	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.DeleteResult{
		DeletedCount: int64(len(list)),
	}, nil
}

// Distinct implements the ICollection.Distinct method.
func (c *Collection) Distinct(ctx context.Context, field string, filter interface{}, opts ...*options.DistinctOptions) ([]interface{}, error) {
	// merge options
	opt := options.MergeDistinctOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"MaxTime": ignored,
	})

	// check field
	if field == "" {
		panic("lungo: missing field path")
	}

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// find documents
	res, err := useTransaction(ctx, c.engine, false, func(txn *Transaction) (interface{}, error) {
		return txn.Find(c.handle, query, nil, 0, 0)
	})
	if err != nil {
		return nil, err
	}

	// get list
	list := res.(*Result).Matched

	// collect distinct values
	values := mongokit.Distinct(list, field)

	return values, nil
}

// Drop implements the ICollection.Drop method.
func (c *Collection) Drop(ctx context.Context) error {
	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return err
	}

	// ensure abortion
	defer c.engine.Abort(txn)

	// drop namespace
	err = txn.Drop(c.handle)
	if err != nil {
		return err
	}

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return err
	}

	return nil
}

// EstimatedDocumentCount implements the ICollection.EstimatedDocumentCount method.
func (c *Collection) EstimatedDocumentCount(ctx context.Context, opts ...*options.EstimatedDocumentCountOptions) (int64, error) {
	// merge options
	opt := options.MergeEstimatedDocumentCountOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"MaxTime": ignored,
	})

	// count documents
	res, err := useTransaction(ctx, c.engine, false, func(txn *Transaction) (interface{}, error) {
		return txn.CountDocuments(c.handle)
	})
	if err != nil {
		return 0, err
	}

	return int64(res.(int)), nil
}

// Find implements the ICollection.Find method.
func (c *Collection) Find(ctx context.Context, filter interface{}, opts ...*options.FindOptions) (ICursor, error) {
	// merge options
	opt := options.MergeFindOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"AllowPartialResults": ignored,
		"BatchSize":           ignored,
		"Comment":             ignored,
		"Limit":               supported,
		"MaxAwaitTime":        ignored,
		"MaxTime":             ignored,
		"NoCursorTimeout":     ignored,
		"Projection":          supported,
		"Skip":                supported,
		"Snapshot":            ignored,
		"Sort":                supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// get sort
	var sort bsonkit.Doc
	if opt.Sort != nil {
		sort, err = bsonkit.Transform(opt.Sort)
		if err != nil {
			return nil, err
		}
	}

	// get projection
	var projection bsonkit.Doc
	if opt.Projection != nil {
		projection, err = bsonkit.Transform(opt.Projection)
		if err != nil {
			return nil, err
		}
	}

	// get skip
	var skip int
	if opt.Skip != nil {
		skip = int(*opt.Skip)
	}

	// get limit
	var limit int
	if opt.Limit != nil {
		limit = int(*opt.Limit)
	}

	// find documents
	res, err := useTransaction(ctx, c.engine, false, func(txn *Transaction) (interface{}, error) {
		return txn.Find(c.handle, query, sort, skip, limit)
	})
	if err != nil {
		return nil, err
	}

	// get list
	list := res.(*Result).Matched

	// apply projection
	if projection != nil {
		list, err = mongokit.ProjectList(list, projection)
		if err != nil {
			return nil, err
		}
	}

	return &Cursor{list: list}, nil
}

// FindOne implements the ICollection.FindOne method.
func (c *Collection) FindOne(ctx context.Context, filter interface{}, opts ...*options.FindOneOptions) ISingleResult {
	// merge options
	opt := options.MergeFindOneOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"AllowPartialResults": ignored,
		"BatchSize":           ignored,
		"Comment":             ignored,
		"MaxAwaitTime":        ignored,
		"MaxTime":             ignored,
		"NoCursorTimeout":     ignored,
		"Projection":          supported,
		"Skip":                supported,
		"Snapshot":            ignored,
		"Sort":                supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return &SingleResult{err: err}
	}

	// get sort
	var sort bsonkit.Doc
	if opt.Sort != nil {
		sort, err = bsonkit.Transform(opt.Sort)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// get skip
	var skip int
	if opt.Skip != nil {
		skip = int(*opt.Skip)
	}

	// get projection
	var projection bsonkit.Doc
	if opt.Projection != nil {
		projection, err = bsonkit.Transform(opt.Projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// find documents
	res, err := useTransaction(ctx, c.engine, false, func(txn *Transaction) (interface{}, error) {
		return txn.Find(c.handle, query, sort, skip, 1)
	})
	if err != nil {
		return &SingleResult{err: err}
	}

	// get list
	list := res.(*Result).Matched

	// check list
	if len(list) == 0 {
		return &SingleResult{}
	}

	// apply projection
	if projection != nil {
		list, err = mongokit.ProjectList(list, projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	return &SingleResult{doc: list[0]}
}

// FindOneAndDelete implements the ICollection.FindOneAndDelete method.
func (c *Collection) FindOneAndDelete(ctx context.Context, filter interface{}, opts ...*options.FindOneAndDeleteOptions) ISingleResult {
	// merge options
	opt := options.MergeFindOneAndDeleteOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"MaxTime":    ignored,
		"Projection": supported,
		"Sort":       supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return &SingleResult{err: err}
	}

	// get projection
	var projection bsonkit.Doc
	if opt.Projection != nil {
		projection, err = bsonkit.Transform(opt.Projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// get sort
	var sort bsonkit.Doc
	if opt.Sort != nil {
		sort, err = bsonkit.Transform(opt.Sort)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// delete documents
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Delete(c.handle, query, sort, 0, 1)
	})
	if err != nil {
		return &SingleResult{err: err}
	}

	// get list
	list := res.(*Result).Matched

	// check list
	if len(list) == 0 {
		return &SingleResult{}
	}

	// apply projection
	if projection != nil {
		list, err = mongokit.ProjectList(list, projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	return &SingleResult{doc: list[0]}
}

// FindOneAndReplace implements the ICollection.FindOneAndReplace method.
func (c *Collection) FindOneAndReplace(ctx context.Context, filter, replacement interface{}, opts ...*options.FindOneAndReplaceOptions) ISingleResult {
	// merge options
	opt := options.MergeFindOneAndReplaceOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"MaxTime":        ignored,
		"Projection":     supported,
		"ReturnDocument": supported,
		"Sort":           supported,
		"Upsert":         supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// check replacement
	if replacement == nil {
		panic("lungo: missing replacement document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return &SingleResult{err: err}
	}

	// get projection
	var projection bsonkit.Doc
	if opt.Projection != nil {
		projection, err = bsonkit.Transform(opt.Projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// get sort
	var sort bsonkit.Doc
	if opt.Sort != nil {
		sort, err = bsonkit.Transform(opt.Sort)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// transform document
	repl, err := bsonkit.Transform(replacement)
	if err != nil {
		return &SingleResult{err: err}
	}

	// get upsert
	var upsert bool
	if opt.Upsert != nil {
		upsert = *opt.Upsert
	}

	// get return after
	var returnAfter bool
	if opt.ReturnDocument != nil {
		returnAfter = *opt.ReturnDocument == options.After
	}

	// insert document
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Replace(c.handle, query, sort, repl, upsert)
	})
	if err != nil {
		return &SingleResult{err: err}
	}

	// get result
	result := res.(*Result)

	// get doc
	var doc bsonkit.Doc
	if result.Upserted != nil {
		if returnAfter {
			doc = result.Upserted
		}
	} else if len(result.Modified) > 0 {
		doc = result.Matched[0]
		if returnAfter {
			doc = result.Modified[0]
		}
	}

	// apply projection
	if doc != nil && projection != nil {
		doc, err = mongokit.Project(doc, projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	return &SingleResult{doc: doc}
}

// FindOneAndUpdate implements the ICollection.FindOneAndUpdate method.
func (c *Collection) FindOneAndUpdate(ctx context.Context, filter, update interface{}, opts ...*options.FindOneAndUpdateOptions) ISingleResult {
	// merge options
	opt := options.MergeFindOneAndUpdateOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"MaxTime":        ignored,
		"Projection":     supported,
		"ReturnDocument": supported,
		"Sort":           supported,
		"Upsert":         supported,
		"ArrayFilters":   supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// check update
	if update == nil {
		panic("lungo: missing update document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return &SingleResult{err: err}
	}

	// get projection
	var projection bsonkit.Doc
	if opt.Projection != nil {
		projection, err = bsonkit.Transform(opt.Projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// get sort
	var sort bsonkit.Doc
	if opt.Sort != nil {
		sort, err = bsonkit.Transform(opt.Sort)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// transform document
	upd, err := bsonkit.Transform(update)
	if err != nil {
		return &SingleResult{err: err}
	}

	// get upsert
	var upsert bool
	if opt.Upsert != nil {
		upsert = *opt.Upsert
	}

	// get return after
	var returnAfter bool
	if opt.ReturnDocument != nil {
		returnAfter = *opt.ReturnDocument == options.After
	}

	// get array filters
	var arrayFilters bsonkit.List
	if opt.ArrayFilters != nil && opt.ArrayFilters.Filters != nil {
		arrayFilters, err = bsonkit.TransformList(opt.ArrayFilters.Filters)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	// update documents
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Update(c.handle, query, sort, upd, 0, 1, upsert, arrayFilters)
	})
	if err != nil {
		return &SingleResult{err: err}
	}

	// get result
	result := res.(*Result)

	// get doc
	var doc bsonkit.Doc
	if result.Upserted != nil {
		if returnAfter {
			doc = result.Upserted
		}
	} else if len(result.Modified) > 0 {
		doc = result.Matched[0]
		if returnAfter {
			doc = result.Modified[0]
		}
	}

	// apply projection
	if doc != nil && projection != nil {
		doc, err = mongokit.Project(doc, projection)
		if err != nil {
			return &SingleResult{err: err}
		}
	}

	return &SingleResult{doc: doc}
}

// Indexes implements the ICollection.Indexes method.
func (c *Collection) Indexes() IIndexView {
	return &IndexView{
		handle: c.handle,
		engine: c.engine,
	}
}

// InsertMany implements the ICollection.InsertMany method.
func (c *Collection) InsertMany(ctx context.Context, documents []interface{}, opts ...*options.InsertManyOptions) (*mongo.InsertManyResult, error) {
	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}

	// merge options
	opt := options.MergeInsertManyOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"Ordered": supported,
	})

	// check documents
	if len(documents) == 0 {
		panic("lungo: missing documents")
	}

	// prepare list
	list := make(bsonkit.List, 0, len(documents))

	// transform documents
	for _, document := range documents {
		// transform document
		doc, err := bsonkit.Transform(document)
		if err != nil {
			return nil, err
		}

		// add to list
		list = append(list, doc)
	}

	// get ordered
	var ordered bool
	if opt.Ordered != nil {
		ordered = *opt.Ordered
	}

	// insert documents
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Insert(c.handle, list, ordered)
	})
	if err != nil {
		return nil, err
	}

	// get result
	result := res.(*Result)

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.InsertManyResult{
		InsertedIDs: bsonkit.Pick(result.Modified, "_id", false),
	}, result.Error
}

// InsertOne implements the ICollection.InsertOne method.
func (c *Collection) InsertOne(ctx context.Context, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}
	// merge options
	opt := options.MergeInsertOneOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{})

	// check document
	if document == nil {
		panic("lungo: missing document")
	}

	// transform document
	doc, err := bsonkit.Transform(document)
	if err != nil {
		return nil, err
	}

	// insert document
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Insert(c.handle, bsonkit.List{doc}, true)
	})
	if err != nil {
		return nil, err
	}

	// get result
	result := res.(*Result)

	// check error
	if result.Error != nil {
		return nil, result.Error
	}

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.InsertOneResult{
		InsertedID: bsonkit.Get(result.Modified[0], "_id"),
	}, nil
}

// Name implements the ICollection.Name method.
func (c *Collection) Name() string {
	return c.handle[1]
}

// ReplaceOne implements the ICollection.ReplaceOne method.
func (c *Collection) ReplaceOne(ctx context.Context, filter, replacement interface{}, opts ...*options.ReplaceOptions) (*mongo.UpdateResult, error) {
	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}
	// merge options
	opt := options.MergeReplaceOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"Upsert": supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// check replacement
	if replacement == nil {
		panic("lungo: missing replacement document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// transform document
	doc, err := bsonkit.Transform(replacement)
	if err != nil {
		return nil, err
	}

	// get upsert
	var upsert bool
	if opt.Upsert != nil {
		upsert = *opt.Upsert
	}

	// insert document
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Replace(c.handle, query, nil, doc, upsert)
	})
	if err != nil {
		return nil, err
	}

	// get result
	result := res.(*Result)

	// check if upserted
	if result.Upserted != nil {
		return &mongo.UpdateResult{
			UpsertedCount: 1,
			UpsertedID:    bsonkit.Get(result.Upserted, "_id"),
		}, nil
	}

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.UpdateResult{
		MatchedCount:  int64(len(result.Matched)),
		ModifiedCount: int64(len(result.Modified)),
	}, nil
}

// UpdateByID implements the ICollection.UpdateByID method.
func (c *Collection) UpdateByID(ctx context.Context, id interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	// check id
	if id == nil {
		return nil, mongo.ErrNilValue
	}

	return c.UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update, opts...)
}

// UpdateMany implements the ICollection.UpdateMany method.
func (c *Collection) UpdateMany(ctx context.Context, filter, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}
	// merge options
	opt := options.MergeUpdateOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"Upsert":       supported,
		"ArrayFilters": supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// check update
	if update == nil {
		panic("lungo: missing update document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// transform document
	doc, err := bsonkit.Transform(update)
	if err != nil {
		return nil, err
	}

	// get upsert
	var upsert bool
	if opt.Upsert != nil {
		upsert = *opt.Upsert
	}

	// get array filters
	var arrayFilters bsonkit.List
	if opt.ArrayFilters != nil && opt.ArrayFilters.Filters != nil {
		arrayFilters, err = bsonkit.TransformList(opt.ArrayFilters.Filters)
		if err != nil {
			return nil, err
		}
	}

	// update documents
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Update(c.handle, query, nil, doc, 0, 0, upsert, arrayFilters)
	})
	if err != nil {
		return nil, err
	}

	// get result
	result := res.(*Result)

	// check if upserted
	if result.Upserted != nil {
		return &mongo.UpdateResult{
			UpsertedCount: 1,
			UpsertedID:    bsonkit.Get(result.Upserted, "_id"),
		}, nil
	}

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.UpdateResult{
		MatchedCount:  int64(len(result.Matched)),
		ModifiedCount: int64(len(result.Modified)),
	}, nil
}

// UpdateOne implements the ICollection.UpdateOne method.
func (c *Collection) UpdateOne(ctx context.Context, filter, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {
	// begin transaction
	txn, err := c.engine.Begin(ctx, true)
	if err != nil {
		return nil, err
	}

	// merge options
	opt := options.MergeUpdateOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"Upsert":       supported,
		"ArrayFilters": supported,
	})

	// check filer
	if filter == nil {
		panic("lungo: missing filter document")
	}

	// check update
	if update == nil {
		panic("lungo: missing update document")
	}

	// transform filter
	query, err := bsonkit.Transform(filter)
	if err != nil {
		return nil, err
	}

	// transform document
	doc, err := bsonkit.Transform(update)
	if err != nil {
		return nil, err
	}

	// get upsert
	var upsert bool
	if opt.Upsert != nil {
		upsert = *opt.Upsert
	}

	// get array filters
	var arrayFilters bsonkit.List
	if opt.ArrayFilters != nil && opt.ArrayFilters.Filters != nil {
		arrayFilters, err = bsonkit.TransformList(opt.ArrayFilters.Filters)
		if err != nil {
			return nil, err
		}
	}

	// update documents
	res, err := useTransaction(ctx, c.engine, true, func(txn *Transaction) (interface{}, error) {
		return txn.Update(c.handle, query, nil, doc, 0, 1, upsert, arrayFilters)
	})
	if err != nil {
		return nil, err
	}

	// get result
	result := res.(*Result)

	// check if upserted
	if result.Upserted != nil {
		return &mongo.UpdateResult{
			UpsertedCount: 1,
			UpsertedID:    bsonkit.Get(result.Upserted, "_id"),
		}, nil
	}

	// commit transaction
	err = c.engine.Commit(txn)
	if err != nil {
		return nil, err
	}

	return &mongo.UpdateResult{
		MatchedCount:  int64(len(result.Matched)),
		ModifiedCount: int64(len(result.Modified)),
	}, nil
}

// Watch implements the ICollection.Watch method.
func (c *Collection) Watch(_ context.Context, pipeline interface{}, opts ...*options.ChangeStreamOptions) (IChangeStream, error) {
	// merge options
	opt := options.MergeChangeStreamOptions(opts...)

	// assert supported options
	assertOptions(opt, map[string]string{
		"BatchSize":            ignored,
		"FullDocument":         ignored,
		"MaxAwaitTime":         ignored,
		"ResumeAfter":          supported,
		"StartAtOperationTime": supported,
		"StartAfter":           supported,
	})

	// transform pipeline
	filter, err := bsonkit.TransformList(pipeline)
	if err != nil {
		return nil, err
	}

	// get resume after
	var resumeAfter bsonkit.Doc
	if opt.ResumeAfter != nil {
		resumeAfter, err = bsonkit.Transform(opt.ResumeAfter)
		if err != nil {
			return nil, err
		}
	}

	// get start after
	var startAfter bsonkit.Doc
	if opt.StartAfter != nil {
		startAfter, err = bsonkit.Transform(opt.StartAfter)
		if err != nil {
			return nil, err
		}
	}

	// open stream
	stream, err := c.engine.Watch(c.handle, filter, resumeAfter, startAfter, opt.StartAtOperationTime)
	if err != nil {
		return nil, err
	}

	return stream, nil
}
