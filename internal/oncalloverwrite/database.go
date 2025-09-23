package oncalloverwrite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	// OverwriteJournal is used to keep track of emergency-duty-overwrites.
	OverwriteJournal = "dutyRosterOverwrites"

	// InboundNumberCollection is the name of the MongoDB collection that
	// stores inbound numbers.
	InboundNumberCollection = "inboundNumbers"
)

// Database is the database interface for the duty rosters.
type Database interface {
	// CreateOverwrite configures an emergency doctor-on-duty overwrite for the
	// given date.
	CreateOverwrite(ctx context.Context, creatorId string, from, to time.Time, user, phone, displayName, inboundNumber string) (structs.Overwrite, error)

	// GetOverwrite returns the currently active overwrite for the given date/time.
	GetActiveOverwrite(ctx context.Context, date time.Time, inboundNumbers []string) (*structs.Overwrite, error)

	// GetOverwrite returns a single overwrite identified by id. Even entries that are marked as deleted
	// will be returned.
	GetOverwrite(ctx context.Context, id string) (*structs.Overwrite, error)

	// GetOverwrites returns all overwrites that have start or time between from and to.
	GetOverwrites(ctx context.Context, from, to time.Time, includeDeleted bool, inboundNumbers []string) ([]*structs.Overwrite, error)

	// DeleteOverwrite deletes the roster overwrite for the given
	// day.
	DeleteActiveOverwrite(ctx context.Context, date time.Time, inboundNumber []string) (*structs.Overwrite, error)

	// DeleteOverwrite deletes the roster overwrite with the given ID
	DeleteOverwrite(ctx context.Context, id string) (*structs.Overwrite, error)

	// CreateInboundNumber creates a new inbound number
	CreateInboundNumber(ctx context.Context, model structs.InboundNumber) error

	// DeleteInboundNumber deletes an existing inbound number.
	DeleteInboundNumber(ctx context.Context, number string) error

	// UpdateInboundNumber updates the display name of an existing inbound number
	UpdateInboundNumber(ctx context.Context, model structs.InboundNumber) error

	// ListInboundNumbers returns a list of existing inbound numbers.
	ListInboundNumbers(ctx context.Context) ([]structs.InboundNumber, error)

	// GetInboundNumber returns the inbound number for the given number.
	GetInboundNumber(ctx context.Context, number string) (structs.InboundNumber, error)
}

type database struct {
	cli            *mongo.Client
	overwrites     *mongo.Collection
	inboundNumbers *mongo.Collection
}

// New is like new but directly accepts the mongoDB client to use.
func New(ctx context.Context, dbName string, client *mongo.Client) (Database, error) {
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}

	db := &database{
		cli:            client,
		overwrites:     client.Database(dbName).Collection(OverwriteJournal),
		inboundNumbers: client.Database(dbName).Collection(InboundNumberCollection),
	}

	if err := db.setup(ctx); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *database) setup(ctx context.Context) error {
	_, err := db.overwrites.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "from", Value: 1},
			{Key: "to", Value: 1},
		},
		// we don't use a unique index here because we only "mark" overwrites
		// as deleted instead of actually deleting them.
		Options: options.Index().SetUnique(false).SetSparse(false),
	})
	if err != nil {
		return fmt.Errorf("%s: failed to create from-to index: %w", OverwriteJournal, err)
	}

	_, err = db.overwrites.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "createdAt", Value: -1},
		},
		Options: options.Index().SetUnique(false).SetSparse(false),
	})
	if err != nil {
		return fmt.Errorf("%s: failed to create createdAt index: %w", OverwriteJournal, err)
	}

	_, err = db.overwrites.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "inboundNumber", Value: 1},
		},
		Options: options.Index().SetSparse(true),
	})
	if err != nil {
		return fmt.Errorf("%s: failed to create inboundNumber index: %w", OverwriteJournal, err)
	}

	_, err = db.inboundNumbers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "number", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("%s: failed to create number index: %w", InboundNumberCollection, err)
	}

	return nil
}

func (db *database) CreateOverwrite(ctx context.Context, creatorId string, from, to time.Time, user, phone, displayName, inboundNumber string) (structs.Overwrite, error) {
	if user == "" && phone == "" {
		return structs.Overwrite{}, fmt.Errorf("username and phone number not set")
	}

	overwrite := structs.Overwrite{
		From:          from,
		To:            to,
		UserID:        user,
		PhoneNumber:   phone,
		DisplayName:   displayName,
		CreatedAt:     time.Now(),
		CreatedBy:     creatorId,
		Deleted:       false,
		InboundNumber: inboundNumber,
	}

	log := log.L(ctx).With(
		"from", from,
		"to", to,
		"user", user,
		"phone", phone,
		"inboundNumber", inboundNumber,
	)

	if res, err := db.overwrites.InsertOne(ctx, overwrite); err == nil {
		overwrite.ID = res.InsertedID.(primitive.ObjectID)
	} else {
		return structs.Overwrite{}, fmt.Errorf("failed to insert overwrite: %w", err)
	}

	target := "tel:" + overwrite.PhoneNumber + " <" + overwrite.DisplayName + ">"
	if overwrite.UserID != "" {
		target = "user:" + overwrite.UserID
	}

	log.With(
		"from", overwrite.From,
		"to", overwrite.To,
		"target", target,
		"createdBy", creatorId,
	).Info("created new roster overwrite")

	return overwrite, nil
}

func (db *database) GetOverwrites(ctx context.Context, filterFrom, filterTo time.Time, includeDeleted bool, inboundNumbers []string) ([]*structs.Overwrite, error) {
	var timeFilter bson.M

	switch {
	case filterFrom.IsZero() && filterTo.IsZero(): // no time range

	case !filterFrom.IsZero() && filterTo.IsZero(): // only from is set so include all entries that end after from
		timeFilter = bson.M{
			"to": bson.M{
				"$gte": filterFrom,
			},
		}

	case filterFrom.IsZero() && !filterTo.IsZero(): // only to is set so include all entries that end before to
		timeFilter = bson.M{
			"to": bson.M{
				"$lte": filterTo,
			},
		}

	default: // both are set
		timeFilter = bson.M{
			"$or": bson.A{
				// all entries that span over the requested time range
				bson.M{
					"from": bson.M{
						"$lte": filterFrom,
					},
					"to": bson.M{
						"$gte": filterTo,
					},
				},

				// all entries that start within the range
				bson.M{
					"from": bson.M{
						"$gte": filterFrom,
						"$lte": filterTo,
					},
				},

				// all entries that end within the range
				bson.M{
					"to": bson.M{
						"$gte": filterFrom,
						"$lte": filterTo,
					},
				},
			},
		}
	}

	if len(inboundNumbers) > 0 {
		timeFilter = bson.M{
			"$and": bson.A{
				timeFilter,
				bson.M{
					"$or": getInboundNumbersFilter(inboundNumbers),
				},
			},
		}
	}
	if !includeDeleted {
		timeFilter["deleted"] = bson.M{"$ne": true}
	}

	opts := options.Find().SetSort(bson.D{
		{Key: "from", Value: 1},
		{Key: "to", Value: 1},
		{Key: "_id", Value: 1},
	})

	res, err := db.overwrites.Find(ctx, timeFilter, opts)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}

	var result []*structs.Overwrite
	if err := res.All(ctx, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (db *database) GetOverwrite(ctx context.Context, id string) (*structs.Overwrite, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, err
	}

	res := db.overwrites.FindOne(ctx, bson.M{"_id": oid})
	if res.Err() != nil {
		return nil, res.Err()
	}

	var result structs.Overwrite
	if err := res.Decode(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (db *database) GetActiveOverwrite(ctx context.Context, date time.Time, inboundNumbers []string) (*structs.Overwrite, error) {
	log.L(ctx).Debug("[active-overwrite] searching database ...")

	opts := options.FindOne().
		SetSort(bson.D{
			{Key: "createdAt", Value: -1},
		})

	res := db.overwrites.FindOne(ctx, bson.M{
		"from": bson.M{
			"$lte": date,
		},
		"to": bson.M{
			"$gt": date,
		},
		"$or":     getInboundNumbersFilter(inboundNumbers),
		"deleted": bson.M{"$ne": true},
	}, opts)

	if res.Err() != nil {
		return nil, res.Err()
	}

	var o structs.Overwrite
	if err := res.Decode(&o); err != nil {
		return nil, err
	}
	return &o, nil
}

func getInboundNumbersFilter(inboundNumbers []string) bson.A {
	inboundNumbersFilter := bson.A{
		bson.M{
			"inboundNumber": bson.M{
				"$exists": false,
			},
		},
	}

	if len(inboundNumbers) > 0 {
		inboundNumbersFilter = append(inboundNumbersFilter, bson.M{
			"inboundNumber": bson.M{
				"$in": inboundNumbers,
			},
		})
	}

	return inboundNumbersFilter
}

func (db *database) DeleteActiveOverwrite(ctx context.Context, d time.Time, inboundNumbers []string) (*structs.Overwrite, error) {
	opts := options.FindOneAndUpdate().
		SetSort(bson.D{
			{Key: "createdAt", Value: -1},
		}).SetReturnDocument(options.After)

	res := db.overwrites.FindOneAndUpdate(
		ctx,
		bson.M{
			"from": bson.M{
				"$lte": d,
			},
			"to": bson.M{
				"$gt": d,
			},
			"deleted": bson.M{"$ne": true},
			"$or":     getInboundNumbersFilter(inboundNumbers),
		},
		bson.M{
			"$set": bson.M{
				"deleted": true,
			},
		},
		opts,
	)

	if res.Err() != nil {
		return nil, res.Err()
	}

	var ov structs.Overwrite
	if err := res.Decode(&ov); err != nil {
		return nil, fmt.Errorf("failed to decode overwrite: %w", err)
	}

	return &ov, nil
}

func (db *database) DeleteOverwrite(ctx context.Context, id string) (*structs.Overwrite, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, fmt.Errorf("failed to parse overwrite id: %w", err)
	}
	res := db.overwrites.FindOneAndUpdate(
		ctx,
		bson.M{
			"_id":     oid,
			"deleted": bson.M{"$ne": true},
		},
		bson.M{
			"$set": bson.M{
				"deleted": true,
			},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	)

	if res.Err() != nil {
		return nil, res.Err()
	}

	var ov structs.Overwrite

	if err := res.Decode(&ov); err != nil {
		return nil, fmt.Errorf("failed to decode overwrite: %w", err)
	}

	return &ov, nil
}

func (db *database) CreateInboundNumber(ctx context.Context, model structs.InboundNumber) error {
	model.ID = primitive.NewObjectIDFromTimestamp(time.Now())

	_, err := db.inboundNumbers.InsertOne(ctx, model)
	if err != nil {
		return fmt.Errorf("failed to perform insert: %w", err)
	}

	return nil
}

func (db *database) DeleteInboundNumber(ctx context.Context, number string) error {
	res, err := db.inboundNumbers.DeleteOne(ctx, bson.M{
		"number": number,
	})
	if err != nil {
		return fmt.Errorf("failed to perform delete: %w", err)
	}

	if res.DeletedCount == 0 {
		return mongo.ErrNoDocuments
	}

	return nil
}

func (db *database) UpdateInboundNumber(ctx context.Context, model structs.InboundNumber) error {
	res, err := db.inboundNumbers.ReplaceOne(ctx, bson.M{
		"number": model.Number,
	}, model)
	if err != nil {
		return fmt.Errorf("failed to perform update: %w", err)
	}

	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}

	return nil
}

func (db *database) ListInboundNumbers(ctx context.Context) ([]structs.InboundNumber, error) {
	res, err := db.inboundNumbers.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to find numbers: %w", err)
	}

	var result []structs.InboundNumber
	if err := res.All(ctx, &result); err != nil {
		return result, fmt.Errorf("failed to decode: %w", err)
	}

	return result, nil
}

func (db *database) GetInboundNumber(ctx context.Context, number string) (structs.InboundNumber, error) {
	var result structs.InboundNumber

	res := db.inboundNumbers.FindOne(ctx, bson.M{
		"number": number,
	})
	if res.Err() != nil {
		return result, fmt.Errorf("failed to find number: %w", res.Err())
	}

	if err := res.Decode(&result); err != nil {
		return result, fmt.Errorf("failed to decode: %w", err)
	}

	return result, nil
}
