package database

import (
	"context"
	"fmt"
	"time"

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
	CreateUnidentified(ctx context.Context, log structs.CallLog) error

	// RecordCustomerCall records a call that has been associated with a customer.
	// When called, RecordCustomerCall searches for an "unidentified" calllog that
	// was recorded at the same time and replaces that entry.
	RecordCustomerCall(ctx context.Context, record structs.CallLog) error

	// Search searches for all records that match query.
	Search(ctx context.Context, query *SearchQuery) ([]structs.CallLog, error)
}

type database struct {
	collection *mongo.Collection
	country    string
}

// New creates a new client.
func New(ctx context.Context, dbName, country string, cli *mongo.Client) (Database, error) {
	db := &database{
		collection: cli.Database(dbName).Collection("callogs"),
		country:    country,
	}

	if err := db.setup(ctx); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *database) setup(ctx context.Context) error {
	_, err := db.collection.Indexes().CreateMany(ctx, []mongo.IndexModel{
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

func (db *database) CreateUnidentified(ctx context.Context, record structs.CallLog) error {
	if record.ID.IsZero() {
		record.ID = primitive.NewObjectID()
	}

	if err := db.perpareRecord(ctx, &record); err != nil {
		return err
	}

	_, err := db.collection.InsertOne(ctx, record)
	if err != nil {
		return fmt.Errorf("failed to insert document: %w", err)
	}

	return nil
}

func (db *database) RecordCustomerCall(ctx context.Context, record structs.CallLog) error {
	if record.ID.IsZero() {
		record.ID = primitive.NewObjectID()
	}

	log := log.L(ctx)
	if err := db.perpareRecord(ctx, &record); err != nil {
		return err
	}

	// load all records that happened on the same date with the same caller
	opts := options.Find().SetSort(bson.M{
		"date": -1,
	})
	filter := bson.M{
		"datestr": record.DateStr,
		"caller":  record.Caller,
	}
	log.Infof("searching for %+v", filter)
	cursor, err := db.collection.Find(ctx, filter, opts)
	if err != nil {
		return fmt.Errorf("failed to retrieve documents: %w", err)
	}
	defer cursor.Close(ctx)

	// we accept any records that happened +- 2 minutes
	lower := record.Date.Add(-2 * time.Minute)
	upper := record.Date.Add(+2 * time.Minute)
	var found bool
	var existing structs.CallLog

	for cursor.Next(ctx) {
		if err := cursor.Decode(&existing); err != nil {
			log.Errorf("failed to decode existing calllog record: %s", err)

			continue
		}

		if lower.Before(existing.Date) && upper.After(existing.Date) {
			found = true

			break
		}
	}
	// we only log error here and still create the record.
	if cursor.Err() != nil {
		log.Errorf("failed to search for unidentified calllog record: %s", cursor.Err())
	}

	if found {
		// copy existing values to the new record
		record.ID = existing.ID
		record.InboundNumber = existing.InboundNumber

		result := db.collection.FindOneAndReplace(ctx, bson.M{"_id": record.ID}, record)
		if result.Err() != nil {
			return fmt.Errorf("failed to find and replace document %s: %w", record.ID, result.Err())
		}

		log.Infof("replaced unidentified calllog for %s with customer-record for %s:%s", record.Caller, record.CustomerSource, record.CustomerID)
	} else {
		_, err := db.collection.InsertOne(ctx, record)
		if err != nil {
			return fmt.Errorf("failed to insert document: %w", err)
		}

		log.Infof("created new customer-record for %s:%s with phone number %s", record.CustomerSource, record.CustomerID, record.Caller)
	}

	return nil
}

func (db *database) Search(ctx context.Context, query *SearchQuery) ([]structs.CallLog, error) {
	filter := query.Build()
	log.L(ctx).Infof("Searching callogs for %+v", filter)

	opts := options.Find().SetSort(bson.M{"date": -1})
	cursor, err := db.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve documents: %w", err)
	}
	defer cursor.Close(ctx)

	var records []structs.CallLog
	if err := cursor.All(ctx, &records); err != nil {
		return nil, fmt.Errorf("failed to decode documents: %w", err)
	}

	return records, nil
}

func (db *database) perpareRecord(ctx context.Context, record *structs.CallLog) error {
	var formattedNumber string
	if record.Caller != "Anonymous" {
		parsed, err := phonenumbers.Parse(record.Caller, db.country)
		if err != nil {
			log.L(ctx).Errorf("failed to parse caller phone number %s: %s", record.Caller, err)
			return err
		}
		formattedNumber = phonenumbers.Format(parsed, phonenumbers.INTERNATIONAL)
	} else {
		formattedNumber = "anonymous"
	}

	record.Caller = formattedNumber
	record.DateStr = record.Date.Format("2006-01-02")
	return nil
}
