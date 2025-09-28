package database

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/nyaruka/phonenumbers"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Database supports storing and retrieving of calllog records.
type Database interface {
	// CreateUnidentified creates new "unidentified" calllog record where
	// we don't know the caller.
	CreateUnidentified(ctx context.Context, log *structs.CallLog) error

	// RecordCustomerCall records a call that has been associated with a customer.
	// When called, RecordCustomerCall searches for an "unidentified" calllog that
	// was recorded at the same time and replaces that entry.
	RecordCustomerCall(ctx context.Context, record *structs.CallLog) error

	// Search searches for all records that match query.
	Search(ctx context.Context, query *SearchQuery) ([]structs.CallLog, error)

	Search2(ctx context.Context, opts ...QueryOption) ([]structs.CallLog, error)

	StreamSearch(ctx context.Context, query *SearchQuery) (<-chan structs.CallLog, <-chan error)

	FindDistinctNumbersWithoutCustomers(ctx context.Context) ([]string, error)

	UpdateUnmatchedNumber(ctx context.Context, number string, customerId string) error
}

type callRecordDatabase struct {
	callRecords *mongo.Collection
	country     string
}

// New creates a new client.
func New(ctx context.Context, dbName, country string, cli *mongo.Client) (Database, error) {
	db := &callRecordDatabase{
		callRecords: cli.Database(dbName).Collection("callogs"),
		country:     country,
	}

	if err := db.setup(ctx); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *callRecordDatabase) setup(ctx context.Context) error {
	_, err := db.callRecords.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "datestr", Value: 1},
			},
			Options: options.Index().SetSparse(false),
		},
		{
			Keys: bson.D{
				{Key: "caller", Value: 1},
			},
			Options: options.Index().SetSparse(false),
		},
		{
			Keys: bson.D{
				{Key: "customerID", Value: 1},
				{Key: "customerSource", Value: 1},
			},
			Options: options.Index().SetSparse(true),
		},
		{
			Keys: bson.D{
				{Key: "agent", Value: 1},
			},
			Options: options.Index().SetSparse(true),
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	return nil
}

func (db *callRecordDatabase) Search(ctx context.Context, search *SearchQuery) ([]structs.CallLog, error) {
	resCh, errCh := db.StreamSearch(ctx, search)

	var (
		results []structs.CallLog
		errors  = new(multierror.Error)
	)

L:
	for {
		select {
		case r, ok := <-resCh:
			if !ok {
				break L
			}
			results = append(results, r)

		case e, ok := <-errCh:
			if !ok {
				break L
			}

			errors.Errors = append(errors.Errors, e)
		}
	}

	return results, errors.ErrorOrNil()
}

func (db *callRecordDatabase) CreateUnidentified(ctx context.Context, record *structs.CallLog) error {
	if record.ID.IsZero() {
		record.ID = primitive.NewObjectID()
	}

	if err := db.perpareRecord(ctx, record); err != nil {
		return err
	}

	res, err := db.callRecords.InsertOne(ctx, record)
	if err != nil {
		return fmt.Errorf("failed to insert document: %w", err)
	}

	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		record.ID = oid
	}

	return nil
}

func (db *callRecordDatabase) UpdateUnmatchedNumber(ctx context.Context, number string, customerId string) error {
	res, err := db.callRecords.UpdateMany(ctx, bson.M{
		"caller": number,
		"customerSource": bson.M{
			"$exists": false,
		},
		"customerID": bson.M{
			"$exists": false,
		},
	}, bson.M{
		"$set": bson.M{
			"customerSource": "",
			"customerID":     customerId,
		},
	})

	if err != nil {
		return fmt.Errorf("failed to update customers: %w", err)
	}

	log.L(ctx).Info("unmatched customer entries updated successfully", "updateCount", res.ModifiedCount)

	return nil
}

func (db *callRecordDatabase) FindDistinctNumbersWithoutCustomers(ctx context.Context) ([]string, error) {
	res, err := db.callRecords.Distinct(ctx, "caller", bson.M{
		"customerSource": bson.M{
			"$exists": false,
		},
		"customerID": bson.M{
			"$exists": false,
		},
	})

	if err != nil {
		return nil, err
	}

	var result = make([]string, 0, len(res))

	for _, r := range res {
		if s, ok := r.(string); ok {
			result = append(result, s)
		}
	}

	return result, nil
}

func (db *callRecordDatabase) RecordCustomerCall(ctx context.Context, record *structs.CallLog) error {
	if record.ID.IsZero() {
		record.ID = primitive.NewObjectID()
	}

	log := log.L(ctx)
	if err := db.perpareRecord(ctx, record); err != nil {
		return err
	}

	// load all records that happened on the same date with the same caller
	opts := options.Find().SetSort(bson.M{
		"date": -1,
	})
	filter := bson.M{
		"datestr": record.DateStr,
		"caller":  record.Caller,
		"durationSeconds": bson.M{
			"$exists": false,
		},
	}

	cursor, err := db.callRecords.Find(ctx, filter, opts)
	if err != nil {
		return fmt.Errorf("failed to retrieve documents: %w", err)
	}
	defer cursor.Close(ctx)

	// we accept any records that happened +/- 2 minutes
	lower := record.Date.Add(-2 * time.Minute)
	upper := record.Date.Add(+2 * time.Minute)
	var found bool
	var existing structs.CallLog

	for cursor.Next(ctx) {
		if err := cursor.Decode(&existing); err != nil {
			log.Error("failed to decode existing calllog record", "error", err)

			continue
		}

		if lower.Before(existing.Date) && upper.After(existing.Date) {
			found = true

			break
		}
	}
	// we only log error here and still create the record.
	if cursor.Err() != nil {
		log.Error("failed to search for unidentified calllog records", "error", cursor.Err())
	}

	if found {
		// copy existing values to the new record
		record.ID = existing.ID
		record.TransferTarget = existing.TransferTarget
		record.Error = existing.Error
		record.TransferFrom = existing.TransferFrom
		record.CallID = existing.CallID

		if record.InboundNumber == "" {
			record.InboundNumber = existing.InboundNumber
		}

		if record.CustomerID == "" {
			record.CustomerID = existing.CustomerID
		}

		if record.FromType == "" {
			record.FromType = existing.FromType
		}
		if record.ToType == "" {
			record.ToType = existing.ToType
		}

		result := db.callRecords.FindOneAndReplace(ctx, bson.M{"_id": record.ID}, record)
		if result.Err() != nil {
			return fmt.Errorf("failed to find and replace document %s: %w", record.ID, result.Err())
		}

		log.Info("replaced unidentified calllog customer-record", "caller", record.Caller, "customerSource", record.CustomerSource, "customerId", record.CustomerID, "record", record)
	} else {
		res, err := db.callRecords.InsertOne(ctx, record)
		if err != nil {
			return fmt.Errorf("failed to insert document: %w", err)
		}

		if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
			res.InsertedID = oid
		}

		log.Info("created new customer-record", "customerSource", record.CustomerSource, "customerId", record.CustomerID, "caller", record.Caller)
	}

	return nil
}

func (db *callRecordDatabase) Search2(ctx context.Context, opts ...QueryOption) ([]structs.CallLog, error) {
	var q query

	for _, opt := range opts {
		opt(&q)
	}

	res, err := db.callRecords.Find(ctx, q.build())
	if err != nil {
		return nil, err
	}

	var results []structs.CallLog
	if err := res.All(ctx, &results); err != nil {
		return nil, err
	}

	return results, nil
}

func (db *callRecordDatabase) StreamSearch(ctx context.Context, query *SearchQuery) (<-chan structs.CallLog, <-chan error) {

	results := make(chan structs.CallLog, 1)
	errs := make(chan error, 1)

	filter := query.Build()
	log.L(ctx).Debug("searching for call-log records", "filter", filter)

	opts := options.Find().SetSort(bson.M{"date": -1})
	cursor, err := db.callRecords.Find(ctx, filter, opts)
	if err != nil {
		errs <- fmt.Errorf("failed to retrieve documents: %w", err)

		return results, errs
	}

	go func() {
		defer close(results)
		defer close(errs)
		defer cursor.Close(ctx)
		for cursor.Next(ctx) {
			var result structs.CallLog

			if err := cursor.Decode(&result); err != nil {
				errs <- err
			} else {
				results <- result
			}
		}
	}()

	return results, errs
}

func (db *callRecordDatabase) perpareRecord(ctx context.Context, record *structs.CallLog) error {
	formattedNumber := record.Caller

	if record.Caller != "Anonymous" {
		/*
			var callerType string
			if record.Direction == "Inbound" {
				callerType = record.ToType
			} else {
				callerType = record.FromType
			}

			if callerType != "extension" {
		*/
		parsed, err := phonenumbers.Parse(record.Caller, db.country)
		if err != nil {
			log.L(ctx).Error("failed to parse caller phone number", "caller", record.Caller, "error", err)
			return err
		}
		formattedNumber = phonenumbers.Format(parsed, phonenumbers.INTERNATIONAL)
		//}
	} else {
		formattedNumber = "anonymous"
	}

	record.Caller = formattedNumber
	record.DateStr = record.Date.Format("2006-01-02")

	return nil
}
