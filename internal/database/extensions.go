package database

import (
	"context"
	"fmt"

	"github.com/bufbuild/connect-go"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type ExtensionDatabase interface {
	SavePhoneExtension(context.Context, *pbx3cxv1.PhoneExtension) error
	DeletePhoneExtension(context.Context, string) error
	ListPhoneExtensions(context.Context) ([]*pbx3cxv1.PhoneExtension, error)
}

type extensionDatabase struct {
	col *mongo.Collection
}

type extensionModel struct {
	Extension            string `bson:"extension"`
	Name                 string `bson:"displayName"`
	EligibleForOverwrite bool   `bson:"eligibleForOverwrite"`
}

func NewExtensionDatabase(ctx context.Context, db *mongo.Database) (*extensionDatabase, error) {
	ext := &extensionDatabase{
		col: db.Collection("phone-extensions"),
	}

	if err := ext.setup(ctx); err != nil {
		return nil, err
	}

	return ext, nil
}

func (extDb *extensionDatabase) setup(ctx context.Context) error {
	if _, err := extDb.col.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "extension"},
		},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("failed to setup indexes for phone-extension collection: %w", err)
	}

	return nil
}

func (extDb *extensionDatabase) SavePhoneExtension(ctx context.Context, ext *pbx3cxv1.PhoneExtension) error {
	model := extensionModel{
		Extension:            ext.Extension,
		Name:                 ext.DisplayName,
		EligibleForOverwrite: ext.EligibleForOverwrite,
	}

	if _, err := extDb.col.InsertOne(ctx, model); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return connect.NewError(connect.CodeAlreadyExists, err)
		}

		return fmt.Errorf("failed to perform insert operation")
	}

	return nil
}

func (extDb *extensionDatabase) DeletePhoneExtension(ctx context.Context, ext string) error {
	res, err := extDb.col.DeleteOne(ctx, bson.M{"extension": ext})
	if err != nil {
		return fmt.Errorf("failed to perform delete operation: %w", err)
	}

	if res.DeletedCount == 0 {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("phone-extension does not exist"))
	}

	return nil
}

func (extDb *extensionDatabase) ListPhoneExtensions(ctx context.Context) ([]*pbx3cxv1.PhoneExtension, error) {
	res, err := extDb.col.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to perform find operation: %w", err)
	}

	var result []extensionModel
	if err := res.All(ctx, &result); err != nil {
		return nil, fmt.Errorf("failed to decode documents: %w", err)
	}

	protoResult := make([]*pbx3cxv1.PhoneExtension, len(result))

	for i, r := range result {
		protoResult[i] = &pbx3cxv1.PhoneExtension{
			Extension:            r.Extension,
			DisplayName:          r.Name,
			EligibleForOverwrite: r.EligibleForOverwrite,
		}
	}

	return protoResult, nil
}

var _ ExtensionDatabase = (*extensionDatabase)(nil)
