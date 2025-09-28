package cdr

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
)

type Server interface {
	Start(context.Context) error
}

type TerminationReason string

const (
	TerminationReasonSource = "src_participant_terminated"
	TerminationReasonDest   = "dst_participant_terminated"
)

type Field string

const (
	FieldHistoryID        Field = "historyId"
	FieldCallID           Field = "callId"
	FieldDuration         Field = "duration"      // HH:MM:SS
	FieldTimeStart        Field = "time-start"    // YYYY.MM.DD HH:MM:SS in UTC
	FieldTimeAnswered     Field = "time-answered" // YYYY.MM.DD HH:MM:SS in UTC
	FieldTimeEnd          Field = "time-end"      // YYYY.MM.DD HH:MM:SS in UTC
	FieldReasonTerminated Field = "reason-terminated"
	FieldFromNumber       Field = "from-no"
	FieldToNumber         Field = "to-no"
	FieldFromDN           Field = "from-dn"
	FieldToDN             Field = "to-dn"
	FieldDialNumber       Field = "dial-no"
	FieldReasonChanged    Field = "reason-changed"
	FieldFinalNumber      Field = "final-number"
	FieldFinalDN          Field = "final-dn"
	FieldBillCode         Field = "bill-code"
	FieldChain            Field = "chain"
	FieldFinalType        Field = "final-type"
	FieldFromType         Field = "from-type"
	FieldToType           Field = "to-type"
	FieldFromDispName     Field = "from-dispname"
	FieldToDispName       Field = "to-dispname"
	FieldFinalDispName    Field = "final-dispname"
)

var defaultFieldOrder = []Field{
	FieldHistoryID,
	FieldCallID,
	FieldDuration,
	FieldTimeStart,
	FieldTimeAnswered,
	FieldTimeEnd,
	FieldReasonTerminated,
	FieldFromNumber,
	FieldToNumber,
	FieldFromDN,
	FieldToDN,
	FieldDialNumber,
	FieldReasonChanged,
	FieldFinalNumber,
	FieldFinalDN,
	FieldBillCode,
	FieldChain,
	FieldFinalType,
	FieldFromType,
	FieldToType,
	FieldFromDispName,
	FieldToDispName,
	FieldFinalDispName,
}

type Record struct {
	HistoryID string `json:"historyId"`
	CallID    string `json:"callId"`

	ReasonTerminated TerminationReason `json:"reason-terminated"`

	TimeReceived time.Time `json:"time-start"`
	TimeAnswered time.Time `json:"time-answered"`
	TimeEnd      time.Time `json:"time-end"`

	Chain string `json:"chain"`

	Duration time.Duration `json:"duration"`

	DialNumber string `json:"dial-no"`

	FinalType   structs.Type `json:"final-type"`
	FinalNumber string       `json:"final-number"`

	FromDN     string       `json:"from-dn"`
	FromType   structs.Type `json:"from-type"`
	FromNumber string       `json:"from-no"`

	ToDN     string       `json:"to-dn"`
	ToType   structs.Type `json:"to-type"`
	ToNumber string       `json:"to-no"`
}

func parseTime(t string) (time.Time, error) {
	if t == "" {
		return time.Time{}, nil
	}

	return time.ParseInLocation("2006.01.02 15:04:05", t, time.UTC)
}

// CreateRecordFromCSV creates a new Record from it's CSV representation. order defines the CSV field order.
// If order is nil, defaultFieldOrder will be used
func CreateRecordFromCSV(columns []string, order []Field, log *slog.Logger) (Record, error) {
	// default to the defaultFieldOrder if no order is specified.
	if order == nil {
		order = defaultFieldOrder
	}

	// the lenght of the columns and the other must match, otherwise there's likely
	// an configuration error.
	if len(columns) != len(order) {
		return Record{}, fmt.Errorf("column and configuration order mismatch: column-count=%d expected-count=%d", len(columns), len(order))
	}

	l := log

	// construct the Record by iterating over the columns and setting the respective field
	// in the Record struct based on the field-order as specified in "order".
	var r Record
	for idx, v := range columns {
		field := order[idx]

		l = l.With(string(field), v)

		switch field {
		case FieldHistoryID:
			r.HistoryID = v

		case FieldCallID:
			r.CallID = v

		case FieldReasonTerminated:
			r.ReasonTerminated = TerminationReason(v)

		case FieldTimeStart:
			t, err := parseTime(v)
			if err != nil {
				return r, err
			}
			r.TimeReceived = t

		case FieldTimeAnswered:
			t, err := parseTime(v)
			if err != nil {
				return r, err
			}
			r.TimeAnswered = t

		case FieldTimeEnd:
			t, err := parseTime(v)
			if err != nil {
				return r, err
			}
			r.TimeEnd = t

		case FieldDuration:
			d, err := parseDuration(v)
			if err != nil {
				return r, err
			}
			r.Duration = d

		case FieldChain:
			r.Chain = v

		case FieldDialNumber:
			r.DialNumber = v

		case FieldFinalType:
			r.FinalType = structs.Type(v)

		case FieldFinalNumber:
			r.FinalNumber = v

		case FieldToType:
			r.ToType = structs.Type(v)

		case FieldToNumber:
			r.ToNumber = v

		case FieldFromType:
			r.FromType = structs.Type(v)

		case FieldFromNumber:
			r.FromNumber = v

		default:
			// Field not needed for call-data-records
		}
	}

	l.Info("successfully converted CDR record")

	return r, nil
}

// Inbound returns true if this call record is an inbound call.
func (r Record) Inbound() bool {
	return r.FromType != structs.TypeExtension
}

// Outbound returns true if this call record is an outbound call.
func (r Record) Outbound() bool {
	return !r.Inbound()
}

// Answered returns true if the call has been answered (accepted) by the destination.
// If this is an inbound call record, Answered will returns false if the internal final-type
// of the record is a queue. In case of an IVR, it will still return true since the caller decided
// to not proceed in the IVR.
func (r Record) Answered() bool {
	// if the final inbound type is a queue the call was not answered.
	if r.Inbound() {
		return r.FinalType != structs.TypeQueue
	}

	// otherwise, we need to rely on the time-answered field.
	return !r.TimeAnswered.IsZero()
}

// Rejected returns true if the call has been rejected by the destination.
func (r Record) Rejected() bool {
	return !r.Answered()
}
