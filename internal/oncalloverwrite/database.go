package oncalloverwrite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// OverwriteJournal is used to keep track of emergency-duty-overwrites.
const OverwriteJournal = "dutyRosterOverwrites"

// Database is the database interface for the duty rosters.
type Database interface {
	// CreateOverwrite configures an emergency doctor-on-duty overwrite for the
	// given date.
	CreateOverwrite(ctx context.Context, creatorId string, from, to time.Time, user, phone, displayName string) (structs.Overwrite, error)

	// GetOverwrite returns the currently active overwrite for the given date/time.
	GetActiveOverwrite(ctx context.Context, date time.Time) (*structs.Overwrite, error)

	// GetOverwrite returns a single overwrite identified by id. Even entries that are marked as deleted
	// will be returned.
	GetOverwrite(ctx context.Context, id string) (*structs.Overwrite, error)

	// GetOverwrites returns all overwrites that have start or time between from and to.
	GetOverwrites(ctx context.Context, from, to time.Time, includeDeleted bool) ([]*structs.Overwrite, error)

	// DeleteOverwrite deletes the roster overwrite for the given
	// day.
	DeleteActiveOverwrite(ctx context.Context, date time.Time) error

	// DeleteOverwrite deletes the roster overwrite with the given ID
	DeleteOverwrite(ctx context.Context, id string) error
}

type database struct {
	cli        *mongo.Client
	overwrites *mongo.Collection
}

// New is like new but directly accepts the mongoDB client to use.
func New(ctx context.Context, dbName string, client *mongo.Client) (Database, error) {
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}

	db := &database{
		cli:        client,
		overwrites: client.Database(dbName).Collection(OverwriteJournal),
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
		return fmt.Errorf("failed to create from-to index: %w", err)
	}

	_, err = db.overwrites.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "createdAt", Value: -1},
		},
		Options: options.Index().SetUnique(false).SetSparse(false),
	})
	if err != nil {
		return fmt.Errorf("failed to create createdAt index: %w", err)
	}

	return nil
}

func (db *database) CreateOverwrite(ctx context.Context, creatorId string, from, to time.Time, user, phone, displayName string) (structs.Overwrite, error) {
	if user == "" && phone == "" {
		return structs.Overwrite{}, fmt.Errorf("username and phone number not set")
	}

	overwrite := structs.Overwrite{
		From:        from,
		To:          to,
		UserID:      user,
		PhoneNumber: phone,
		DisplayName: displayName,
		CreatedAt:   time.Now(),
		CreatedBy:   creatorId,
		Deleted:     false,
	}

	log := log.L(ctx).WithFields(logrus.Fields{
		"from":  from,
		"to":    to,
		"user":  user,
		"phone": phone,
	})

	if res, err := db.overwrites.InsertOne(ctx, overwrite); err == nil {
		overwrite.ID = res.InsertedID.(primitive.ObjectID)
	} else {
		return structs.Overwrite{}, fmt.Errorf("failed to insert overwrite: %w", err)
	}

	target := "tel:" + overwrite.PhoneNumber + " <" + overwrite.DisplayName + ">"
	if overwrite.UserID != "" {
		target = "user:" + overwrite.UserID
	}

	log.WithFields(logrus.Fields{
		"from":      overwrite.From,
		"to":        overwrite.To,
		"target":    target,
		"createdBy": creatorId,
	}).Infof("created new roster overwrite")

	return overwrite, nil
}

func (db *database) GetOverwrites(ctx context.Context, filterFrom, filterTo time.Time, includeDeleted bool) ([]*structs.Overwrite, error) {
	filter := bson.M{
		"$or": bson.A{
			bson.M{
				"from": bson.M{
					"$gte": filterFrom,
					"$lt":  filterTo,
				},
			},
			bson.M{
				"to": bson.M{
					"$gt": filterFrom,
					"$lt": filterTo,
				},
			},
			bson.M{
				"from": bson.M{"$lte": filterFrom},
				"to":   bson.M{"$gt": filterTo},
			},
		},
	}
	if !includeDeleted {
		filter["deleted"] = bson.M{"$ne": true}
	}

	opts := options.Find().SetSort(bson.D{
		{Key: "from", Value: 1},
		{Key: "to", Value: 1},
		{Key: "_id", Value: 1},
	})

	res, err := db.overwrites.Find(ctx, filter, opts)
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

func (db *database) GetActiveOverwrite(ctx context.Context, date time.Time) (*structs.Overwrite, error) {
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
		"deleted": bson.M{"$ne": true},
	}, opts)
	log.L(ctx).Debug("[active-overwrite] received result")

	if res.Err() != nil {
		return nil, res.Err()
	}

	log.L(ctx).Debug("[active-overwrite] decoding overwrite")
	var o structs.Overwrite
	if err := res.Decode(&o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (db *database) DeleteActiveOverwrite(ctx context.Context, d time.Time) error {
	opts := options.FindOneAndUpdate().
		SetSort(bson.D{
			{Key: "createdAt", Value: -1},
		})

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
		},
		bson.M{
			"$set": bson.M{
				"deleted": true,
			},
		},
		opts,
	)

	if res.Err() != nil {
		return res.Err()
	}

	return nil
}

func (db *database) DeleteOverwrite(ctx context.Context, id string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return err
	}
	res, err := db.overwrites.UpdateMany(
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
	)

	if err != nil {
		return err
	}

	if res.ModifiedCount == 0 {
		return mongo.ErrNoDocuments
	}

	return nil
}
