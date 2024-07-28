package structs

import (
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type InboundNumber struct {
	ID              primitive.ObjectID `bson:"_id"`
	Number          string             `bson:"number"`
	DisplayName     string             `bson:"display_name,omitempty"`
	RosterTypeName  string             `bson:"roster_type_name,omitempty"`
	RosterShiftTags []string           `bson:"roster_shift_tags,omitempty"`
}

func (in InboundNumber) ToProto() *pbx3cxv1.InboundNumber {
	return &pbx3cxv1.InboundNumber{
		Number:          in.Number,
		DisplayName:     in.DisplayName,
		RosterShiftTags: in.RosterShiftTags,
		RosterTypeName:  in.RosterTypeName,
	}
}
