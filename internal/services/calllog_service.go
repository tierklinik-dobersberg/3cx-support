package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	"github.com/tierklinik-dobersberg/apis/pkg/auth"
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

	// error notification handling for admin users.
	notifyErrorLock sync.Mutex
	notifyOnce      sync.Once
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
		Date:           time.Now().Local(),
		Caller:         caller,
		InboundNumber:  inboundNumber,
		TransferTarget: transferTo,
	}

	if isError != "" {
		parsedBool, err := strconv.ParseBool(isError)
		if err == nil {
			record.Error = parsedBool
		} else {
			log.L(req.Context()).Errorf("failed to parse error parameter %v: %s", isError, err)
		}
	}

	if err := svc.CallLogDB.CreateUnidentified(req.Context(), record); err != nil {
		log.L(req.Context()).Errorf("failed to create unidentified call-log entry: %s", err)
	} else {
		log.L(req.Context()).Infof("successfully created unidentified call log entry: %#v", record)
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

func (svc *CallService) GetOnCall(ctx context.Context, req *connect.Request[pbx3cxv1.GetOnCallRequest]) (*connect.Response[pbx3cxv1.GetOnCallResponse], error) {
	dateTime := time.Now()

	if req.Msg.Date != "" {
		var err error
		dateTime, err = time.Parse(time.RFC3339, req.Msg.Date)
		if err != nil {
			return svc.handleOnCallError(ctx, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("request: invalid value for date: %q: %w", req.Msg.Date, err)))
		}
	}

	response, err := svc.resolveOnCallTarget(ctx, dateTime, req.Msg.IgnoreOverwrites, req.Msg.InboundNumber)
	if err != nil {
		return svc.handleOnCallError(ctx, err)
	}

	go svc.resetErrorNotification()

	return response, nil
}

func (svc *CallService) CreateOverwrite(ctx context.Context, req *connect.Request[pbx3cxv1.CreateOverwriteRequest]) (*connect.Response[pbx3cxv1.CreateOverwriteResponse], error) {
	remoteUser := auth.From(ctx)
	if remoteUser == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("missing remote user"))
	}

	r := req.Msg

	// prepare the overwrite model
	model := structs.Overwrite{
		From:      r.From.AsTime(),
		To:        r.To.AsTime(),
		CreatedBy: remoteUser.ID,
		CreatedAt: time.Now(),
		InboundNumber: req.Msg.InboundNumber,
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

	// validate the overwrite has a valid target
	target, _, err := svc.resolveOverwriteTarget(ctx, model)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("overwrite does not have a valid target phone number"))
	}
	if model.From.After(model.To) || model.From.Equal(model.To) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("overwrite time-range is invalid"))
	}

	// if this is a direct phone number overwrite, use the santitized value instead.
	if model.PhoneNumber != "" {
		model.DisplayName = target
		model.PhoneNumber = target
	}

	// actually create the overwrite
	model, err = svc.OverwriteDB.CreateOverwrite(ctx, model.CreatedBy, model.From, model.To, model.UserID, model.PhoneNumber, model.DisplayName)
	if err != nil {
		return nil, err
	}

	// notify administrators about the new overwrite.
	go func() {
		what := "all numbers"
		if model.InboundNumber != "" {
			what = model.InboundNumber
		}

		if err := svc.sendNotificationToAdmins(context.Background(), remoteUser.ID, fmt.Sprintf(
			"User {{ .Sender | displayName }} created a new overwrite for %s to %s from %s to %s",
			what,
			target,
			model.From.In(time.Local).Format(time.RFC3339),
			model.To.In(time.Local).Format(time.RFC3339),
		)); err != nil {
			log.L(context.Background()).Errorf("failed to send overwrite creation notice: %s", err)
		}
	}()

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
		err = svc.OverwriteDB.DeleteActiveOverwrite(ctx, v.ActiveAt.AsTime(), req.Msg.InboundNumbers.GetNumbers())

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
		overwrite, err = svc.OverwriteDB.GetActiveOverwrite(ctx, v.ActiveAt.AsTime(), req.Msg.InboundNumbers.GetNumbers())

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
		overwrites, err = svc.OverwriteDB.GetOverwrites(ctx, v.TimeRange.From.AsTime(), v.TimeRange.To.AsTime(), false, req.Msg.InboundNumbers.GetNumbers())

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
		log.L(ctx).Errorf("failed to search for call log entries: %s", query.String())
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

func (svc *CallService) resolveOnCallTarget(ctx context.Context, dateTime time.Time, ignoreOverwrites bool, inboundNumber string) (*connect.Response[pbx3cxv1.GetOnCallResponse], error) {
	var numbers []string
	if inboundNumber != "" {
		numbers = []string{inboundNumber}
	}

	overwrite, err := svc.OverwriteDB.GetActiveOverwrite(ctx, dateTime, numbers)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("database: %w", err)
	}

	if overwrite != nil && !ignoreOverwrites {
		target, profile, err := svc.resolveOverwriteTarget(ctx, *overwrite)
		if err != nil {
			return nil, fmt.Errorf("overwrite: %w", err)
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
			PrimaryTransferTarget: target,
		}

		return connect.NewResponse(res), nil
	}

	workingStaff, err := svc.Roster.GetWorkingStaff(ctx, connect.NewRequest(&rosterv1.GetWorkingStaffRequest{
		Time:           timestamppb.New(dateTime),
		OnCall:         true,
		RosterTypeName: svc.Config.RosterTypeName,
	}))
	if err != nil {
		return nil, fmt.Errorf("roster: failed to get working staff from RosterService: %w", err)
	}

	log.L(ctx).Infof("received response for RosterService.GetWorkingStaff: userIds=%#v rosterIds=%#v", workingStaff.Msg.UserIds, workingStaff.Msg.RosterId)

	if len(workingStaff.Msg.UserIds) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no roster defined for %s", dateTime))
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
		return nil, fmt.Errorf("roster: failed to determine on-call users")
	}

	// Set the primary transfer-target from the first on-call user
	res.PrimaryTransferTarget = res.OnCall[0].TransferTarget

	return connect.NewResponse(res), nil
}

func (svc *CallService) getUserIdForAgent(ctx context.Context, agent string) string {
	profiles, err := svc.Users.ListUsers(ctx, connect.NewRequest(&idmv1.ListUsersRequest{
		FieldMask: &fieldmaskpb.FieldMask{
			Paths: []string{"profiles.user.avatar"},
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

		if extra := p.User.GetExtra().GetFields(); extra != nil {
			if agent == extra["phoneExtension"].GetStringValue() {
				return p.User.Id
			}

			if agent == extra["emergencyExtension"].GetStringValue() {
				return p.User.Id
			}
		}
	}

	return ""
}

func (svc *CallService) handleOnCallError(ctx context.Context, err error) (*connect.Response[pbx3cxv1.GetOnCallResponse], error) {
	remoteUser := auth.From(ctx)
	if remoteUser != nil {
		// Send an error notifcation to all admin users.
		go svc.sendErrorNotification(context.Background(), remoteUser.ID, err)
	} else {
		log.L(ctx).Errorf("failed to get remote user from context")
	}

	// return the fail-over transfer target if one is specified.
	if ft := svc.Config.FailoverTransferTarget; ft != "" {
		log.L(ctx).Errorf("failed to get on-call response, returning failover target %q: %s", ft, err)

		return connect.NewResponse(&pbx3cxv1.GetOnCallResponse{
			PrimaryTransferTarget: ft,
		}), nil
	}

	return nil, err
}

func (svc *CallService) resolveOverwriteTarget(ctx context.Context, overwrite structs.Overwrite) (string, *idmv1.Profile, error) {
	target := overwrite.PhoneNumber
	var profile *idmv1.Profile

	if overwrite.UserID != "" {
		var err error
		profile, err = svc.fetchUserProfile(ctx, overwrite.UserID)
		if err != nil {
			return "", nil, fmt.Errorf("failed to fetch user with id %q: %w", overwrite.UserID, err)
		}

		target = svc.getUserTransferTarget(profile)
	}

	target = strings.ReplaceAll(target, " ", "")
	target = strings.ReplaceAll(target, "-", "")
	target = strings.ReplaceAll(target, "/", "")

	if target == "" {
		return "", nil, fmt.Errorf("failed to get transfer target for overwrite")
	}

	// verify the target is actually a number
	verify := target
	if strings.HasPrefix(target, "+") {
		verify = strings.TrimPrefix(verify, "+")
	}
	if _, err := strconv.ParseInt(verify, 10, 0); err != nil {
		return "", nil, fmt.Errorf("invalid transfer target: expected a number but got %q", target)
	}

	return target, profile, nil
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

func (svc *CallService) sendNotificationToAdmins(ctx context.Context, remoteUserID string, msg string) error {
	// find all idm_superusers
	users, err := svc.Users.ListUsers(ctx, connect.NewRequest(&idmv1.ListUsersRequest{
		FilterByRoles: []string{"idm_superuser"},
		FieldMask: &fieldmaskpb.FieldMask{
			Paths: []string{"profiles.user.id", "profiles.user.username"},
		},
	}))

	if err != nil {
		return fmt.Errorf("failed to resolve users: %w", err)
	}

	// bail out if we failed to find any admin users
	if len(users.Msg.GetUsers()) == 0 {
		return fmt.Errorf("failed to determine users with idm_superuser role")
	}

	// prepare a slice of target user ids
	ids := make([]string, 0, len(users.Msg.Users))
	for _, usr := range users.Msg.Users {
		log.L(ctx).Debugf("sending overwrite creation notice to %q (%s)", usr.GetUser().GetUsername(), usr.GetUser().GetId())

		ids = append(ids, usr.GetUser().GetId())
	}

	// actually send the notification.
	res, err := svc.Notify.SendNotification(ctx, connect.NewRequest(&idmv1.SendNotificationRequest{
		TargetUsers:  ids,
		SenderUserId: remoteUserID,
		Message: &idmv1.SendNotificationRequest_Sms{
			Sms: &idmv1.SMS{
				Body: msg,
			},
		},
	}))

	if err != nil {
		return fmt.Errorf("failed to send notifications: %w", err)
	}

	countFailed := 0
	for _, delivery := range res.Msg.Deliveries {
		if delivery.ErrorKind != idmv1.ErrorKind_ERROR_KIND_UNSPECIFIED {
			countFailed++

			log.L(ctx).Errorf("failed to send error notification to user %q: (%s) %s", delivery.TargetUser, delivery.ErrorKind.String(), delivery.Error)
		}
	}

	if countFailed == len(ids) {
		return fmt.Errorf("failed to notify at least one idm_useruser user")
	}

	return nil
}

func (svc *CallService) sendErrorNotification(ctx context.Context, remoteUserId string, onCallError error) {
	svc.notifyErrorLock.Lock()
	defer svc.notifyErrorLock.Unlock()

	svc.notifyOnce.Do(func() {
		err := svc.sendNotificationToAdmins(ctx, remoteUserId, fmt.Sprintf("failed to get on-call target: %s", onCallError))

		if err != nil {
			log.L(ctx).Errorf("failed to send error notification: %s", err)

			go svc.resetErrorNotification()
		}
	})
}

func (svc *CallService) resetErrorNotification() {
	svc.notifyErrorLock.Lock()
	defer svc.notifyErrorLock.Unlock()

	svc.notifyOnce = sync.Once{}
}
