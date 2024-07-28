package structs

import (
	"time"

	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Overwrite defines an overwrite for the emergency doctor-on-duty
// at a given date.
type Overwrite struct {
	// ID is the a unique ID of the overwrite
	ID primitive.ObjectID `bson:"_id,omitempty" json:"_id"`

	// From holds the datetime at which the overwrite is considered active.
	From time.Time `bson:"from" json:"from"`

	// To holds teh datetime at which the overwrite should not be considered active
	// anymore.
	To time.Time `bson:"to" json:"to"`

	// UserID is the name of the CIS user that is in duty instead.
	UserID string `bson:"userId,omitempty" json:"userId,omitempty"`

	// PhoneNumber is the phone-number that is in duty instead.
	PhoneNumber string `bson:"phoneNumber,omitempty" json:"phoneNumber,omitempty"`

	// DisplayName can be set to a arbitrary value and is used for UI display purposes when
	// duty is changed to a phone-number instead of a user.
	DisplayName string `bson:"displayName,omitempty" json:"displayName,omitempty"`

	// Deleted is set to true if this overwrite has been deleted or superseded.
	Deleted bool `bson:"deleted,omitempty" json:"deleted,omitempty"`

	// CreatedBy is set to the name of the CIS user that created the overwrite.
	CreatedBy string `bson:"createdBy,omitempty" json:"createdBy"`

	// CreatedAt holds the time at which the overwrite has been created.
	CreatedAt time.Time `bson:"createdAt,omitempty" json:"createdAt"`

	// InboundNumber is the inbound number this overwrite relates to.
	InboundNumber string `bson:"inboundNumber,omitempty"`
}

func (ov Overwrite) ToProto() *pbx3cxv1.Overwrite {
	p := &pbx3cxv1.Overwrite{
		Id:              ov.ID.Hex(),
		From:            timestamppb.New(ov.From),
		To:              timestamppb.New(ov.To),
		CreatedAt:       timestamppb.New(ov.CreatedAt),
		CreatedByUserId: ov.CreatedBy,
		InboundNumber: &pbx3cxv1.InboundNumber{
			Number: ov.InboundNumber,
		},
	}

	if ov.PhoneNumber != "" {
		p.Target = &pbx3cxv1.Overwrite_Custom{
			Custom: &pbx3cxv1.CustomOverwrite{
				TransferTarget: ov.PhoneNumber,
				DisplayName:    ov.DisplayName,
			},
		}
	} else {
		p.Target = &pbx3cxv1.Overwrite_UserId{
			UserId: ov.UserID,
		}
	}

	return p
}
