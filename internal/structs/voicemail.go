package structs

import (
	"time"

	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/ql"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type (
	VoiceMail struct {
		ID            primitive.ObjectID `bson:"_id"`
		Mailbox       primitive.ObjectID `bson:"mailboxId"`
		ReceiveTime   time.Time          `bson:"receiveTime"`
		Subject       string             `bson:"subject"`
		Message       string             `bson:"message,omitempty"`
		SeenTime      time.Time          `bson:"seenTime,omitempty"`
		Caller        string             `bson:"caller,omitempty"`
		CustomerId    string             `bson:"customerId,omitempty"`
		FileName      string             `bson:"fileName,omitempty"`
		InboundNumber string             `bson:"inboundNumber,omitempty"`
	}
)

var VoiceMailModel = ql.FieldList{
	ql.FieldSpec{
		Name:         "receiveTime",
		TypeResolver: ql.NullableType(ql.TimeStartKeywordType(time.Local)),
		Aliases:      []string{"received"},
	},
	ql.FieldSpec{
		Name:         "seenTime",
		TypeResolver: ql.NullableType(ql.TimeStartKeywordType(time.Local)),
		Aliases:      []string{"seen"},
	},
	ql.FieldSpec{
		Name:         "caller",
		TypeResolver: ql.NullableType(nil),
	},
	ql.FieldSpec{
		Name:         "customerId",
		TypeResolver: ql.NullableType(nil),
	},
	ql.FieldSpec{
		Name: "inboundNumber",
	},
	ql.FieldSpec{
		Name: "subject",
	},
}

func (vm VoiceMail) ToProto() *pbx3cxv1.VoiceMail {
	pb := &pbx3cxv1.VoiceMail{
		Id:            vm.ID.Hex(),
		Mailbox:       vm.Mailbox.Hex(),
		ReceiveTime:   timestamppb.New(vm.ReceiveTime),
		Subject:       vm.Subject,
		Message:       vm.Message,
		FileName:      vm.FileName,
		InboundNumber: vm.InboundNumber,
	}

	if !vm.SeenTime.IsZero() {
		pb.SeenTime = timestamppb.New(vm.SeenTime)
	}

	if vm.Caller != "" {
		pb.Caller = &pbx3cxv1.VoiceMail_Number{
			Number: vm.Caller,
		}
	} else if vm.CustomerId != "" {
		pb.Caller = &pbx3cxv1.VoiceMail_Customer{
			Customer: &customerv1.Customer{
				Id: vm.CustomerId,
			},
		}
	}

	return pb
}

func (vm *VoiceMail) FromProto(pb *pbx3cxv1.VoiceMail) error {
	if pb.Id != "" {
		oid, err := primitive.ObjectIDFromHex(pb.Id)
		if err != nil {
			return err
		}

		vm.ID = oid
	}

	if pb.Mailbox != "" {
		oid, err := primitive.ObjectIDFromHex(pb.Mailbox)
		if err != nil {
			return err
		}

		vm.Mailbox = oid
	}

	vm.FileName = pb.FileName
	vm.InboundNumber = pb.InboundNumber
	vm.Subject = pb.Subject
	vm.Message = pb.Message
	vm.ReceiveTime = pb.ReceiveTime.AsTime()

	if pb.SeenTime.IsValid() {
		vm.SeenTime = pb.SeenTime.AsTime()
	}

	switch v := pb.Caller.(type) {
	case *pbx3cxv1.VoiceMail_Customer:
		vm.CustomerId = v.Customer.GetId()
	case *pbx3cxv1.VoiceMail_Number:
		vm.Caller = v.Number
	}

	return nil
}
