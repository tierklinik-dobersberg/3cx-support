package structs

import pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"

type Type string

const (
	TypeQueue        Type = "queue"
	TypeExtension    Type = "extension"
	TypeScript       Type = "script"
	TypeExternalLine Type = "external_line"
	TypeIVR          Type = "ivr"
	TypeOutboundRule Type = "outbound_rule"
)

func (t Type) ToProto() pbx3cxv1.ParticipantType {
	switch t {
	case TypeQueue:
		return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_QUEUE
	case TypeExtension:
		return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_EXTENSION
	case TypeScript:
		return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_SCRIPT
	case TypeExternalLine:
		return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_EXTERNAL_LINE
	case TypeIVR:
		return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_IVR
	case TypeOutboundRule:
		return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_OUTBOUND_RULE
	}

	return pbx3cxv1.ParticipantType_PARTICIPANT_TYPE_UNSPECIFIED
}
