package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/3cx-support/internal/database"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	idmv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/idm/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1/pbx3cxv1connect"
	rosterv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/roster/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"go.mongodb.org/mongo-driver/mongo"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type CallService struct {
	pbx3cxv1connect.UnimplementedCallServiceHandler

	*config.Providers
}

func New(p *config.Providers) *CallService {
	return &CallService{
		Providers: p,
	}
}

func (svc *CallService) RecordCallHandler(w http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()

	caller := query.Get("ani")
	inboundNumber := query.Get("did")
	transferTo := query.Get("transferTo")
	isError := query.Get("error")

	record := structs.CallLog{
		Date:           time.Now(),
		Caller:         caller,
		InboundNumber:  inboundNumber,
		TransferTarget: transferTo,
	}

	if isError != "" {
		parsedBool, err := strconv.ParseBool(isError)
		if err == nil {
			record.Error = parsedBool
		} else {
			log.L(req.Context()).Errorf("failed to parse error parameter %v: %w", isError, err)
		}
	}

	if err := svc.CallLogDB.CreateUnidentified(req.Context(), record); err != nil {
		log.L(req.Context()).Errorf("failed to create unidentified call-log entry: %w", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

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

func (svc *CallService) getUserIdForAgent(ctx context.Context, agent string) string {

	profiles, err := svc.Users.ListUsers(ctx, connect.NewRequest(&idmv1.ListUsersRequest{
		FieldMask: &fieldmaskpb.FieldMask{
			Paths: []string{"user.avatar"},
		},
		ExcludeFields: true,
	}))
	if err != nil {
		logrus.Errorf("failed to fetch users from idm service: %s", err)
	}

	for _, p := range profiles.Msg.Users {
		if agent == p.User.GetPrimaryPhoneNumber().GetNumber() {
			return p.User.Id
		}

		if p.User.GetExtra() != nil {
			if agent == p.User.GetExtra().GetFields()["phoneExtension"].GetStringValue() {
				return p.User.Id
			}

			if agent == p.User.GetExtra().GetFields()["emergencyExtension"].GetStringValue() {
				return p.User.Id
			}
		}
	}

	return ""
}

func (svc *CallService) handleOnCallError(ctx context.Context, err error) (*connect.Response[pbx3cxv1.GetOnCallResponse], error) {
	// return the fail-over transfer target if one is specified
	if ft := svc.Config.FailoverTransferTarget; ft != "" {
		log.L(ctx).Errorf("failed to get on-call response, returning failover target %q: %s", ft, err)

		return connect.NewResponse(&pbx3cxv1.GetOnCallResponse{
			PrimaryTransferTarget: ft,
		}), nil
	}

	return nil, err
}

func (svc *CallService) GetOnCall(ctx context.Context, req *connect.Request[pbx3cxv1.GetOnCallRequest]) (*connect.Response[pbx3cxv1.GetOnCallResponse], error) {
	dateTime := time.Now()

	if req.Msg.Date != "" {
		var err error
		dateTime, err = time.Parse(time.RFC3339, req.Msg.Date)
		if err != nil {
			return svc.handleOnCallError(ctx, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value for date: %q: %w", req.Msg.Date, err)))
		}
	}

	overwrite, err := svc.OverwriteDB.GetActiveOverwrite(ctx, dateTime)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return svc.handleOnCallError(ctx, err)
	}

	if overwrite != nil && !req.Msg.IgnoreOverwrites {
		target := overwrite.PhoneNumber
		var profile *idmv1.Profile

		if overwrite.UserID != "" {
			var err error
			profile, err = svc.fetchUserProfile(ctx, overwrite.UserID)
			if err != nil {
				return svc.handleOnCallError(ctx, fmt.Errorf("failed to fetch user with id %q: %w", overwrite.UserID, err))
			}

			target = svc.getUserTransferTarget(profile)
		}

		if target == "" {
			return svc.handleOnCallError(ctx, fmt.Errorf("failed to get transfer target for overwrite"))
		}

		res := &pbx3cxv1.GetOnCallResponse{
			IsOverwrite: true,
			OnCall: []*pbx3cxv1.OnCall{
				{
					TransferTarget: target,
					Profile:        profile,
					Until:          timestamppb.New(overwrite.To),
				},
			},
		}

		return connect.NewResponse(res), nil
	}

	workingStaff, err := svc.Roster.GetWorkingStaff(ctx, connect.NewRequest(&rosterv1.GetWorkingStaffRequest{
		Time:           timestamppb.New(dateTime),
		OnCall:         true,
		RosterTypeName: svc.Config.RosterTypeName,
	}))
	if err != nil {
		return svc.handleOnCallError(ctx, fmt.Errorf("failed to get working staff from RosterService: %w", err))
	}

	log.L(ctx).Infof("received response for RosterService.GetWorkingStaff: userIds=%#v rosterIds=%#v", workingStaff.Msg.UserIds, workingStaff.Msg.RosterId)

	if len(workingStaff.Msg.UserIds) == 0 {
		return svc.handleOnCallError(ctx, connect.NewError(connect.CodeNotFound, fmt.Errorf("no roster defined for %s", dateTime)))
	}

	res := &pbx3cxv1.GetOnCallResponse{
		IsOverwrite: false,
	}

	for _, userId := range workingStaff.Msg.UserIds {
		profile, err := svc.fetchUserProfile(ctx, userId)
		if err != nil {
			log.L(ctx).Errorf("failed to fetch user with id %q: %s", userId, err)

			continue
		}

		var until time.Time
		for _, shift := range workingStaff.Msg.CurrentShifts {
			if !slices.Contains(shift.AssignedUserIds, userId) {
				continue
			}

			if shift.To.AsTime().Before(until) || until.IsZero() {
				until = shift.To.AsTime()
			}
		}

		target := svc.getUserTransferTarget(profile)
		if target != "" {
			res.OnCall = append(res.OnCall, &pbx3cxv1.OnCall{
				Profile:        profile,
				TransferTarget: target,
				Until:          timestamppb.New(until),
			})
		} else {
			log.L(ctx).Warnf("user %q (id=%q) marked as on-call but no transfer target available", profile.User.Username, userId)
		}
	}

	if len(res.OnCall) == 0 {
		return svc.handleOnCallError(ctx, fmt.Errorf("failed to determine on-call users"))
	}

	// Set the primary transfer-target from the first on-call user
	res.PrimaryTransferTarget = res.OnCall[0].TransferTarget

	return connect.NewResponse(res), nil
}

func (svc *CallService) CreateOverwrite(ctx context.Context, req *connect.Request[pbx3cxv1.CreateOverwriteRequest]) (*connect.Response[pbx3cxv1.CreateOverwriteResponse], error) {
	r := req.Msg

	model := structs.Overwrite{
		From:      r.From.AsTime(),
		To:        r.To.AsTime(),
		CreatedBy: req.Header().Get("X-Remote-User-ID"),
		CreatedAt: time.Now(),
	}

	switch v := r.TransferTarget.(type) {
	case *pbx3cxv1.CreateOverwriteRequest_UserId:
		model.UserID = v.UserId
	case *pbx3cxv1.CreateOverwriteRequest_Custom:
		model.DisplayName = v.Custom.DisplayName
		model.PhoneNumber = v.Custom.TransferTarget
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or unsupported transfer_target"))
	}

	model, err := svc.OverwriteDB.CreateOverwrite(ctx, model.CreatedBy, model.From, model.To, model.UserID, model.PhoneNumber, model.DisplayName)
	if err != nil {
		return nil, err
	}

	res := &pbx3cxv1.CreateOverwriteResponse{
		Overwrite: model.ToProto(),
	}

	return connect.NewResponse(res), nil
}

func (svc *CallService) DeleteOverwrite(ctx context.Context, req *connect.Request[pbx3cxv1.DeleteOverwriteRequest]) (*connect.Response[pbx3cxv1.DeleteOverwriteResponse], error) {
	var err error

	switch v := req.Msg.Selector.(type) {
	case *pbx3cxv1.DeleteOverwriteRequest_OverwriteId:
		err = svc.OverwriteDB.DeleteOverwrite(ctx, v.OverwriteId)
	case *pbx3cxv1.DeleteOverwriteRequest_ActiveAt:
		err = svc.OverwriteDB.DeleteActiveOverwrite(ctx, v.ActiveAt.AsTime())

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or unsupported selector"))
	}

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("failed to delete overwrite"))
		}

		return nil, err
	}

	return connect.NewResponse(&pbx3cxv1.DeleteOverwriteResponse{}), nil
}

func (svc *CallService) GetOverwrite(ctx context.Context, req *connect.Request[pbx3cxv1.GetOverwriteRequest]) (*connect.Response[pbx3cxv1.GetOverwriteResponse], error) {
	var (
		overwrites []*structs.Overwrite
		err        error
	)

	switch v := req.Msg.Selector.(type) {
	case *pbx3cxv1.GetOverwriteRequest_ActiveAt:
		var overwrite *structs.Overwrite
		overwrite, err = svc.OverwriteDB.GetActiveOverwrite(ctx, v.ActiveAt.AsTime())

		overwrites = []*structs.Overwrite{overwrite}

	case *pbx3cxv1.GetOverwriteRequest_OverwriteId:
		var overwrite *structs.Overwrite
		overwrite, err = svc.OverwriteDB.GetOverwrite(ctx, v.OverwriteId)

		// ignore delete overwrites here
		if overwrite != nil && overwrite.Deleted {
			overwrite = nil
			err = mongo.ErrNoDocuments
		}

		overwrites = []*structs.Overwrite{overwrite}

	case *pbx3cxv1.GetOverwriteRequest_TimeRange:
		overwrites, err = svc.OverwriteDB.GetOverwrites(ctx, v.TimeRange.From.AsTime(), v.TimeRange.To.AsTime(), false)

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or unsupported selector"))
	}

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, err
	}

	res := &pbx3cxv1.GetOverwriteResponse{
		Overwrites: make([]*pbx3cxv1.Overwrite, len(overwrites)),
	}

	for idx, ov := range overwrites {
		res.Overwrites[idx] = ov.ToProto()
	}

	return connect.NewResponse(res), nil
}

func (svc *CallService) GetLogsForDate(ctx context.Context, req *connect.Request[pbx3cxv1.GetLogsForDateRequest]) (*connect.Response[pbx3cxv1.GetLogsForDateResponse], error) {
	date, err := time.ParseInLocation("2006-01-02", req.Msg.Date, time.Local)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value for date: %w", err))
	}

	query := new(database.SearchQuery).
		AtDate(date)

	logs, err := svc.CallLogDB.Search(ctx, query)
	if err != nil {
		return nil, err
	}

	res := &pbx3cxv1.GetLogsForDateResponse{
		Results: make([]*pbx3cxv1.CallEntry, len(logs)),
	}

	for idx, log := range logs {
		res.Results[idx] = log.ToProto()
	}

	return connect.NewResponse(res), nil
}

func (svc *CallService) GetLogsForCustomer(ctx context.Context, req *connect.Request[pbx3cxv1.GetLogsForCustomerRequest]) (*connect.Response[pbx3cxv1.GetLogsForCustomerResponse], error) {
	query := new(database.SearchQuery).
		Customer(req.Msg.Source, req.Msg.Id)

	logs, err := svc.CallLogDB.Search(ctx, query)
	if err != nil {
		return nil, err
	}

	res := &pbx3cxv1.GetLogsForCustomerResponse{
		Results: make([]*pbx3cxv1.CallEntry, len(logs)),
	}

	for idx, log := range logs {
		res.Results[idx] = log.ToProto()
	}

	return connect.NewResponse(res), nil
}

func (svc *CallService) SearchCallLogs(ctx context.Context, req *connect.Request[pbx3cxv1.SearchCallLogsRequest]) (*connect.Response[pbx3cxv1.SearchCallLogsResponse], error) {
	query := new(database.SearchQuery)

	if req.Msg.CustomerRef != nil {
		query.Customer(req.Msg.CustomerRef.Source, req.Msg.CustomerRef.Id)
	}

	if tr := req.Msg.TimeRange; tr != nil {
		query.Between(tr.From.AsTime(), tr.To.AsTime())
	} else if req.Msg.Date != "" {
		parsed, err := time.ParseInLocation("2006-01-02", req.Msg.Date, time.Local)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid value for date: %w", err))
		}
		query.AtDate(parsed)
	}

	logs, err := svc.CallLogDB.Search(ctx, query)
	if err != nil {
		return nil, err
	}

	res := &pbx3cxv1.SearchCallLogsResponse{
		Results: make([]*pbx3cxv1.CallEntry, len(logs)),
	}

	for idx, log := range logs {
		res.Results[idx] = log.ToProto()
	}

	return connect.NewResponse(res), nil
}

func (svc *CallService) fetchUserProfile(ctx context.Context, userId string) (*idmv1.Profile, error) {
	profile, err := svc.Users.GetUser(ctx, connect.NewRequest(&idmv1.GetUserRequest{
		Search: &idmv1.GetUserRequest_Id{
			Id: userId,
		},
		FieldMask: &fieldmaskpb.FieldMask{
			Paths: []string{"profile.user.avatar"},
		},
		ExcludeFields: true,
	}))

	if err != nil {
		return nil, fmt.Errorf("failed to fetch user with id %q: %w", userId, err)
	}

	return profile.Msg.GetProfile(), nil
}

func (svc *CallService) getUserTransferTarget(profile *idmv1.Profile) string {
	if extrapb := profile.GetUser().GetExtra(); extrapb != nil {
		for _, key := range svc.Config.UserPhoneExtensionKeys {
			phoneExtension, ok := extrapb.Fields[key]

			if !ok {
				continue
			}

			switch v := phoneExtension.Kind.(type) {
			case *structpb.Value_StringValue:
				return v.StringValue
			case *structpb.Value_NumberValue:
				return fmt.Sprintf("%d", int(v.NumberValue))
			default:
				logrus.Warnf("unsupported value type %T for phoneExtension key %q", phoneExtension.Kind, key)
			}
		}
	}

	if pp := profile.GetUser().GetPrimaryPhoneNumber(); pp != nil {
		return pp.Number
	}

	return ""
}
