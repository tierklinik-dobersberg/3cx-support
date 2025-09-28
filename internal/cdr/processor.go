package cdr

import (
	"context"
	"log/slog"
	"math"
	"strings"

	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
)

// Processor is capable of processing a CSV style call-data-record (CDR).
type Processor interface {
	Process(ctx context.Context, line []string, log *slog.Logger)
}

// CallRecord is responsible for persiting an incoming or outgoing call to a customer.
type CallRecorder interface {
	RecordCustomerCall(context.Context, *structs.CallLog) error
}

type UserAgentResolver interface {
	GetUserIdForAgent(ctx context.Context, agent string) string
}

// ProcessorImpl implements the Processor interface using a given
// CSV field ordering and a call recorder.
type ProcessorImpl struct {
	order        []Field
	recorder     CallRecorder
	userResolver UserAgentResolver
}

// NewProcessor creates and returns a new CDR CSV processor using the provided
// fieldOrder and the call recorder.
func NewProcessor(fieldOrder []Field, recorder CallRecorder, userResolver UserAgentResolver) *ProcessorImpl {
	return &ProcessorImpl{
		order:        fieldOrder,
		recorder:     recorder,
		userResolver: userResolver,
	}
}

// Process implements Processor and handles an incoming CDR CSV row.
func (p *ProcessorImpl) Process(ctx context.Context, line []string, log *slog.Logger) {
	record, err := CreateRecordFromCSV(line, p.order, log)
	if err != nil {
		log.Error("failed to convert call-data-record", "error", err, "data", strings.Join(line, ","))
		return
	}

	cr, err := p.callLogFromRecord(ctx, record)
	if err != nil {
		log.Error("failed to construct call-log-record from CDR", "error", err)
		return
	}

	if err := p.recorder.RecordCustomerCall(context.Background(), &cr); err != nil {
		log.Error("failed to process call-data-record", "error", err, "data", strings.Join(line, ","))
	}
}

func (p *ProcessorImpl) callLogFromRecord(ctx context.Context, r Record) (structs.CallLog, error) {
	cr := structs.CallLog{}

	cr.DateStr = r.TimeReceived.Format("2006-01-02")
	cr.Date = r.TimeReceived
	cr.CallID = r.CallID
	cr.DurationSeconds = uint64(math.Floor(r.Duration.Seconds()))
	cr.FromType = string(r.FromType)
	cr.ToType = string(r.FinalType)
	cr.Chain = r.Chain

	if r.Inbound() {
		cr.Caller = r.FromNumber
		cr.Direction = "Inbound"
		cr.InboundNumber = r.DialNumber
		cr.Agent = r.FinalNumber

		if r.Answered() {
			cr.CallType = "Inbound"
		} else {
			cr.CallType = "Missed"
		}
	} else {
		cr.Caller = r.DialNumber
		cr.Direction = "Outbound"
		cr.Agent = r.FromNumber

		if r.Answered() {
			cr.CallType = "Outbound"
		} else {
			cr.CallType = "NotAnswered"
		}
	}

	cr.AgentUserId = p.userResolver.GetUserIdForAgent(ctx, cr.Agent)

	return cr, nil
}
