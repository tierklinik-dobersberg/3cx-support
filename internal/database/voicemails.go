package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/mailsync"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsonrw"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type MailboxDatabase interface {
	CreateMailbox(ctx context.Context, mailbox *pbx3cxv1.Mailbox) error
	ListMailboxes(ctx context.Context) ([]*pbx3cxv1.Mailbox, error)
	GetMailbox(ctx context.Context, id string) (*pbx3cxv1.Mailbox, error)
	DeleteMailbox(ctx context.Context, id string) error
	AppendNotificationSetting(ctx context.Context, mailbox string, nfs *pbx3cxv1.NotificationSettings) error
	DeleteNotificationSetting(ctx context.Context, mailbox, name string) error
	UpdateMailbox(ctx context.Context, mb *pbx3cxv1.Mailbox) error

	CreateVoiceMail(ctx context.Context, voicemail *pbx3cxv1.VoiceMail) error
	ListVoiceMails(ctx context.Context, mailbox string, query *pbx3cxv1.VoiceMailFilter) ([]*pbx3cxv1.VoiceMail, error)
	MarkVoiceMails(ctx context.Context, seen bool, mailbox string, ids []string) error
	GetVoicemail(ctx context.Context, id string) (*pbx3cxv1.VoiceMail, error)

	mailsync.Store
}

type mailboxDatabase struct {
	mailboxes *mongo.Collection
	records   *mongo.Collection
	syncState *mongo.Collection
}

func NewMailboxDatabase(ctx context.Context, cli *mongo.Database) (MailboxDatabase, error) {
	db := &mailboxDatabase{
		mailboxes: cli.Collection("mailboxes"),
		records:   cli.Collection("voicemail-records"),
		syncState: cli.Collection("sync-states"),
	}

	if err := db.setup(ctx); err != nil {
		return nil, err
	}

	return db, nil
}

func (db *mailboxDatabase) setup(ctx context.Context) error {
	if _, err := db.mailboxes.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "notificationSettings.name", Value: 1},
			},
			Options: options.Index().SetUnique(true),
		},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on mailbox collection: %w", err)
	}

	if _, err := db.records.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "mailbox", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "receiveTime", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "caller", Value: 1},
			},
			Options: options.Index().SetSparse(true),
		},
		{
			Keys: bson.D{
				{Key: "customerId", Value: 1},
			},
			Options: options.Index().SetSparse(true),
		},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on records collection: %w", err)
	}

	return nil
}

func (db *mailboxDatabase) LoadState(ctx context.Context, id string) (*mailsync.State, error) {
	res := db.syncState.FindOne(ctx, bson.M{"name": id})
	if err := res.Err(); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &mailsync.State{}, nil
		}

		return nil, err
	}

	var state mailsync.State
	if err := res.Decode(&state); err != nil {
		return nil, err
	}

	return &state, nil
}

func (db *mailboxDatabase) SaveState(ctx context.Context, state mailsync.State) error {
	_, err := db.syncState.ReplaceOne(ctx, bson.M{"name": state.Name}, state, options.Replace().SetUpsert(true))
	if err != nil {
		return err
	}

	return nil
}

func (db *mailboxDatabase) CreateMailbox(ctx context.Context, mailbox *pbx3cxv1.Mailbox) error {
	m, err := MessageToBSON(mailbox.Id, mailbox)
	if err != nil {
		return err
	}

	res, err := db.mailboxes.InsertOne(ctx, m)
	if err != nil {
		return fmt.Errorf("failed to insert: %w", err)
	}

	mailbox.Id = res.InsertedID.(primitive.ObjectID).Hex()

	return nil
}

func (db *mailboxDatabase) ListMailboxes(ctx context.Context) ([]*pbx3cxv1.Mailbox, error) {
	res, err := db.mailboxes.Find(ctx, bson.M{})
	if err != nil {
		return nil, fmt.Errorf("failed to perform find operation: %w", err)
	}

	var result []*pbx3cxv1.Mailbox
	for res.Next(ctx) {
		mb := new(pbx3cxv1.Mailbox)

		if err := BSONToMessage(res.Current, mb, &mb.Id); err != nil {
			return nil, err
		}

		result = append(result, mb)
	}

	return result, nil
}

func (db *mailboxDatabase) GetMailbox(ctx context.Context, id string) (*pbx3cxv1.Mailbox, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, err
	}

	res := db.mailboxes.FindOne(ctx, bson.M{"_id": oid})
	if err := res.Err(); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}

		return nil, res.Err()
	}

	raw, err := res.Raw()
	if err != nil {
		return nil, err
	}

	mb := new(pbx3cxv1.Mailbox)
	if err := BSONToMessage(raw, mb, &mb.Id); err != nil {
		return nil, err
	}

	return mb, nil
}

func (db *mailboxDatabase) DeleteMailbox(ctx context.Context, id string) error {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return fmt.Errorf("failed to parse mailbox id: %w", err)
	}

	res, err := db.mailboxes.DeleteOne(ctx, bson.M{"_id": oid})
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return ErrNotFound
		}

		return fmt.Errorf("failed to perform delete operation: %w", err)
	}

	if res.DeletedCount == 0 {
		return ErrNotFound
	}

	return nil
}

func (db *mailboxDatabase) UpdateMailbox(ctx context.Context, mb *pbx3cxv1.Mailbox) error {
	doc, err := MessageToBSON(mb.Id, mb)
	if err != nil {
		return fmt.Errorf("failed to convert protobuf message to bson: %w", err)
	}

	res, err := db.mailboxes.ReplaceOne(ctx, bson.M{
		"_id": doc["_id"],
	}, doc)
	if err != nil {
		return fmt.Errorf("failed to perform replace operation: %w", err)
	}

	if res.MatchedCount == 0 {
		return ErrNotFound
	}

	return nil
}

func (db *mailboxDatabase) AppendNotificationSetting(ctx context.Context, mailbox string, setting *pbx3cxv1.NotificationSettings) error {
	oid, err := primitive.ObjectIDFromHex(mailbox)
	if err != nil {
		return fmt.Errorf("failed to parse mailbox id: %w", err)
	}

	filter := bson.M{
		"_id": oid,
	}

	m, err := MessageToBSON("", setting)
	if err != nil {
		return fmt.Errorf("failed to convert protobuf message to bson: %w", err)
	}

	// first, try to replace the value
	replaceResult, err := db.mailboxes.UpdateOne(ctx, filter, bson.M{
		"$set": bson.M{
			"notificationSettings.$[filter]": m,
		},
	}, options.Update().SetArrayFilters(options.ArrayFilters{
		Filters: []any{
			bson.M{
				"filter": bson.M{
					"name": setting.Name,
				},
			},
		},
	}))
	if err != nil {
		return fmt.Errorf("failed to replace notification settings: %w", err)
	}

	// if we modified a document we're done now.
	if replaceResult.ModifiedCount > 0 {
		return nil
	}

	// otherwise, try to push it to the array.

	update := bson.M{
		"$push": bson.M{
			"notificationSettings": m,
		},
	}

	res, err := db.mailboxes.UpdateOne(ctx, filter, update)
	if err != nil {
		// this one is fine as the replacement was the same as the existing one.
		if mongo.IsDuplicateKeyError(err) {
			return nil
		}

		return fmt.Errorf("failed to perform update operation: %w", err)
	}

	if res.MatchedCount == 0 {
		return ErrNotFound
	}

	return nil
}

func (db *mailboxDatabase) DeleteNotificationSetting(ctx context.Context, mailbox, settingName string) error {
	oid, err := primitive.ObjectIDFromHex(mailbox)
	if err != nil {
		return fmt.Errorf("failed to parse mailbox id: %w", err)
	}

	filter := bson.M{
		"_id": oid,
	}

	update := bson.M{
		"$pull": bson.M{
			"notificationSettings": bson.M{
				"name": settingName,
			},
		},
	}

	res, err := db.mailboxes.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to perform update operation: %w", err)
	}

	if res.MatchedCount == 0 {
		return ErrNotFound
	}

	if res.ModifiedCount == 0 {
		return fmt.Errorf("%w: notification-setting with name %q", ErrNotFound, settingName)
	}

	return nil
}

func (db *mailboxDatabase) CreateVoiceMail(ctx context.Context, mail *pbx3cxv1.VoiceMail) error {
	model := new(structs.VoiceMail)

	if err := model.FromProto(mail); err != nil {
		return err
	}

	if model.ID.IsZero() {
		model.ID = primitive.NewObjectID()
	}

	res, err := db.records.InsertOne(ctx, model)
	if err != nil {
		return fmt.Errorf("failed to perform insert operation: %w", err)
	}

	mail.Id = res.InsertedID.(primitive.ObjectID).Hex()

	return nil
}

func (db *mailboxDatabase) ListVoiceMails(ctx context.Context, mailbox string, query *pbx3cxv1.VoiceMailFilter) ([]*pbx3cxv1.VoiceMail, error) {
	oid, err := primitive.ObjectIDFromHex(mailbox)
	if err != nil {
		return nil, err
	}

	filter := bson.M{
		"mailboxId": oid,
	}

	switch v := query.GetCaller().(type) {
	case nil:
		// nothing to do

	case *pbx3cxv1.VoiceMailFilter_CustomerId:
		filter["customerId"] = v.CustomerId

	case *pbx3cxv1.VoiceMailFilter_Number:
		filter["caller"] = v.Number

	default:
		return nil, fmt.Errorf("invalid or unsupported caller query: %T", v)
	}

	v := query.GetTimeRange()
	switch {
	case v == nil:
		// nothing to do

	case v.From.IsValid() && v.To.IsValid():
		if v.To.AsTime().Before(v.From.AsTime()) {
			return nil, fmt.Errorf("invalid time_range value")
		}

		filter["receiveTime"] = bson.M{
			"$gte": v.From.AsTime(),
			"$lte": v.To.AsTime(),
		}

	case v.From.IsValid():
		filter["receiveTime"] = bson.M{
			"$gte": v.From.AsTime(),
		}

	case v.To.IsValid():
		filter["receiveTime"] = bson.M{
			"$lte": v.To.AsTime(),
		}

	default:
		return nil, fmt.Errorf("invalid time_range value")
	}

	unseen := query.GetUnseen()
	switch {
	case unseen == nil:
		// nothing to do
	case unseen.Value:
		filter["seenTime"] = bson.M{
			"$exists": false,
		}

	case !unseen.Value:
		filter["seenTime"] = bson.M{
			"$exists": true,
		}
	}

	res, err := db.records.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to perform find operation: %w", err)
	}

	var models []structs.VoiceMail
	if err := res.All(ctx, &models); err != nil {
		return nil, err
	}

	results := make([]*pbx3cxv1.VoiceMail, len(models))
	for idx, m := range models {
		results[idx] = m.ToProto()
	}

	return results, nil
}

func (db *mailboxDatabase) GetVoicemail(ctx context.Context, id string) (*pbx3cxv1.VoiceMail, error) {
	oid, err := primitive.ObjectIDFromHex(id)
	if err != nil {
		return nil, err
	}

	res := db.records.FindOne(ctx, bson.M{"_id": oid})
	if err := res.Err(); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}

		return nil, res.Err()
	}

	model := new(structs.VoiceMail)
	if err := res.Decode(&model); err != nil {
		return nil, err
	}

	return model.ToProto(), nil
}

func (db *mailboxDatabase) MarkVoiceMails(ctx context.Context, seen bool, mailbox string, ids []string) error {
	oids := make([]primitive.ObjectID, len(ids))
	for idx, id := range ids {
		oid, err := primitive.ObjectIDFromHex(id)
		if err != nil {
			return fmt.Errorf("failed to parse voicemail id: %w", err)
		}

		oids[idx] = oid
	}

	filter := bson.M{}

	if mailbox != "" {
		moid, err := primitive.ObjectIDFromHex(mailbox)
		if err != nil {
			return fmt.Errorf("failed to parse mailbox id: %w", err)
		}

		filter["mailboxId"] = moid
	}

	if len(oids) > 0 {
		filter["_id"] = bson.M{
			"$in": oids,
		}
	}

	filter["seenTime"] = bson.M{
		"$exists": !seen,
	}

	op := bson.M{
		"$set": bson.M{
			"seenTime": time.Now(),
		},
	}

	if !seen {
		op = bson.M{
			"$unset": bson.M{
				"seenTime": "",
			},
		}
	}

	_, err := db.records.UpdateMany(
		ctx,
		filter,
		op,
	)

	if err != nil {
		return err
	}

	return nil
}

func BSONToMessage(document bson.Raw, msg proto.Message, id *string) error {
	var m bson.M
	dec, err := bson.NewDecoder(bsonrw.NewBSONDocumentReader(document))
	if err != nil {
		return err
	}

	if err := dec.Decode(&m); err != nil {
		return err
	}

	blob, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to marshal BSON as JSON: %w", err)
	}

	unmarshaler := protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}

	if err := unmarshaler.Unmarshal(blob, msg); err != nil {
		slog.Error("failed to unmarshal JSON to protobuf message", slog.Any("error", err.Error()), slog.Any("json", string(blob)))

		return fmt.Errorf("failed to unmarshal JSON to protobuf message: %w", err)
	}

	if id != nil {
		*id = m["_id"].(primitive.ObjectID).Hex()
	}

	return nil
}

func MessageToBSON(id string, msg proto.Message) (bson.M, error) {
	opts := protojson.MarshalOptions{
		Multiline: true,
		Indent:    "  ",
	}

	blob, err := opts.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to convert proto.Message to JSON: %w", err)
	}

	vr, err := bsonrw.NewExtJSONValueReader(bytes.NewReader(blob), true)
	if err != nil {
		return nil, fmt.Errorf("failed to create ext. JSON reader: %w", err)
	}
	dec, err := bson.NewDecoder(vr)
	if err != nil {
		return nil, fmt.Errorf("failed to create BSON decoder: %w", err)
	}
	dec.DefaultDocumentM()

	var m bson.M
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("failed to decode extended JSON to BSON: %w", err)
	}

	if id != "" {
		var err error

		m["_id"], err = primitive.ObjectIDFromHex(id)
		if err != nil {
			return nil, fmt.Errorf("failed to parse document id: %w", err)
		}
	}

	return m, nil
}
