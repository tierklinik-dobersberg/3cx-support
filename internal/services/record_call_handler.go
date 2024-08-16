package services

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (svc *CallService) RecordCall(ctx context.Context, req *connect.Request[pbx3cxv1.RecordCallRequest]) (*connect.Response[emptypb.Empty], error) {
	msg := req.Msg

	record := structs.CallLog{
		Caller:         msg.Number,
		Agent:          msg.Agent,
		CallType:       msg.CallType,
		CustomerID:     msg.CustomerId,
		CustomerSource: msg.CustomerSource,
	}

	if msg.Duration != "" {
		durationInSeconds, err := strconv.ParseUint(msg.Duration, 10, 64)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value for duration: %q: %w", msg.Duration, err))
		}

		record.DurationSeconds = durationInSeconds
	}

	date, err := time.ParseInLocation("02.01.2006 15:04", msg.DateTime, time.Local)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value for date-time: %w", err))
	}

	record.Date = date
	record.AgentUserId = svc.getUserIdForAgent(ctx, record.Agent)

	if err := svc.CallLogDB.RecordCustomerCall(ctx, record); err != nil {
		return nil, err
	}

	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (svc *CallService) RecordCallHandler(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()

	caller := query.Get("ani")
	inboundNumber := query.Get("did")
	transferTo := query.Get("transferTo")
	isError := query.Get("error")
	transferFrom := query.Get("from")
	callID := query.Get("callID")

	record := structs.CallLog{
		Date:           time.Now().Local(),
		Caller:         caller,
		InboundNumber:  inboundNumber,
		TransferTarget: transferTo,
		TransferFrom:   transferFrom,
		CallID:         callID,
	}

	if isError != "" {
		parsedBool, err := strconv.ParseBool(isError)
		if err == nil {
			record.Error = parsedBool
		} else {
			log.L(req.Context()).Errorf("failed to parse error parameter %v: %s", isError, err)
		}
	}

	go func() {
		ctx := context.Background()

		// try to search the customer record
		if strings.ToLower(record.Caller) != "anonymous" {
			res, err := svc.Customer.SearchCustomer(ctx, connect.NewRequest(&customerv1.SearchCustomerRequest{
				Queries: []*customerv1.CustomerQuery{
					&customerv1.CustomerQuery{
						Query: &customerv1.CustomerQuery_PhoneNumber{
							PhoneNumber: record.Caller,
						},
					},
				},
			}))

			if err != nil {
				log.L(ctx).Errorf("failed to search customer records for phone number %q: %s", record.Caller, err)
			} else {
				if len(res.Msg.Results) > 0 {
					customer := res.Msg.Results[0].Customer

					log.L(ctx).Infof("identified caller: %s %s (%s)", customer.FirstName, customer.LastName, customer.Id)
					record.CustomerID = customer.Id

					if len(res.Msg.Results) > 1 {
						log.L(ctx).Warnf("found multiple customer records for caller number %q, using first one", record.Caller)
					}
				} else {
					log.L(ctx).Errorf("failed to find customer record for phone number %q", record.Caller)
				}
			}
		}

		if err := svc.CallLogDB.CreateUnidentified(ctx, record); err != nil {
			log.L(req.Context()).Errorf("failed to create unidentified call-log entry: %s", err)
		} else {
			log.L(req.Context()).Infof("successfully created unidentified call log entry: %#v", record)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}
