package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/tierklinik-dobersberg/apis/pkg/auth"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"go.mongodb.org/mongo-driver/mongo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type CallService struct {
	pbx3cxv1connect.UnimplementedCallServiceHandler

	*config.Providers

	// error notification handling for admin users.
	notifyErrorLock sync.Mutex
	notifyOnce      sync.Once

	caches map[string]*OnCallCache
}

func New(p *config.Providers) (*CallService, error) {
	svc := &CallService{
		Providers: p,
		caches:    map[string]*OnCallCache{},
	}

	// fetch all inbound number
	numbers, err := p.OverwriteDB.ListInboundNumbers(context.Background())
	if err != nil {
		slog.Error("failed to fetch inbound numbers", "error", err)
	}

	for _, n := range numbers {
		cache, err := NewOnCallCache(context.Background(), n.Number, p)
		if err != nil {
			slog.Error("failed to create on-call cache", "inboundNumber", n.Number, "error", err)
		} else {
			svc.caches[n.Number] = cache
		}
	}

	return svc, nil
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

	response, err := svc.ResolveOnCallTarget(ctx, dateTime, req.Msg.IgnoreOverwrites, req.Msg.InboundNumber)
	if err != nil {
		return svc.handleOnCallError(ctx, err)
	}

	///// Verification Code
	go func() {
		defer func() {
			if x := recover(); x != nil {
				slog.Error("cought panic", "panic", x)
			}
		}()

		ib := req.Msg.InboundNumber
		if ib == "" {
			ib = svc.Config.DefaultOnCallInboundNumber
		}

		cache, ok := svc.caches[ib]
		if ok {
			cached := cache.Current()

			if proto.Equal(cached, response) {
				slog.Info("cached response is correct")
			} else {
				slog.Info("cache response is incorrect")
				if _, err := svc.Providers.Notify.SendNotification(ctx, connect.NewRequest(&idmv1.SendNotificationRequest{
					Message: &idmv1.SendNotificationRequest_Sms{
						Sms: &idmv1.SMS{
							Body: "cached response is incorrect",
						},
					},
					TargetRoles: []string{
						"itadmin",
					},
				})); err != nil {
					slog.Error("failed to send cache-verification notification", "error", err)
				}
			}
		} else {
			slog.Warn("no on-call cache found for inbound number", "number", ib)
		}
	}()

	///// Verification Code End

	go svc.resetErrorNotification()

	return connect.NewResponse(response), nil
}

func (svc *CallService) CreateOverwrite(ctx context.Context, req *connect.Request[pbx3cxv1.CreateOverwriteRequest]) (*connect.Response[pbx3cxv1.CreateOverwriteResponse], error) {
	remoteUser := auth.From(ctx)
	if remoteUser == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("missing remote user"))
	}

	r := req.Msg

	if !r.From.IsValid() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing from field"))
	}
	if !r.To.IsValid() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing to field"))
	}

	// prepare the overwrite model
	model := structs.Overwrite{
		From:          r.From.AsTime(),
		To:            r.To.AsTime(),
		CreatedBy:     remoteUser.ID,
		CreatedAt:     time.Now(),
		InboundNumber: req.Msg.InboundNumber,
	}

	if model.To.Before(model.From) || model.To.Equal(model.From) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid time range"))
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
	target, _, err := svc.ResolveOverwriteTarget(ctx, model)
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
	model, err = svc.OverwriteDB.CreateOverwrite(ctx, model.CreatedBy, model.From, model.To, model.UserID, model.PhoneNumber, model.DisplayName, model.InboundNumber)
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

	// trigger cache updates
	go func() {
		for _, cache := range svc.caches {
			cache.Trigger()
		}
	}()

	// publish an event to the event service
	if svc.Providers.Events != nil {
		svc.Providers.PublishEvent(&pbx3cxv1.OverwriteCreatedEvent{
			Overwrite: model.ToProto(),
		}, false)
	}

	return connect.NewResponse(res), nil
}

func (svc *CallService) DeleteOverwrite(ctx context.Context, req *connect.Request[pbx3cxv1.DeleteOverwriteRequest]) (*connect.Response[pbx3cxv1.DeleteOverwriteResponse], error) {
	var (
		err error
		ov  *structs.Overwrite
	)

	switch v := req.Msg.Selector.(type) {
	case *pbx3cxv1.DeleteOverwriteRequest_OverwriteId:
		ov, err = svc.OverwriteDB.DeleteOverwrite(ctx, v.OverwriteId)
	case *pbx3cxv1.DeleteOverwriteRequest_ActiveAt:
		ov, err = svc.OverwriteDB.DeleteActiveOverwrite(ctx, v.ActiveAt.AsTime(), req.Msg.InboundNumbers.GetNumbers())

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid or unsupported selector"))
	}

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("failed to delete overwrite"))
		}

		return nil, err
	}

	// trigger cache updates
	go func() {
		for _, cache := range svc.caches {
			cache.Trigger()
		}
	}()

	svc.Providers.PublishEvent(&pbx3cxv1.OverwriteDeletedEvent{
		Overwrite: ov.ToProto(),
	}, false)

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
		var (
			from time.Time
			to   time.Time
		)

		if v.TimeRange.To.IsValid() {
			to = v.TimeRange.To.AsTime()
		}

		if v.TimeRange.From.IsValid() {
			from = v.TimeRange.From.AsTime()
		}

		overwrites, err = svc.OverwriteDB.GetOverwrites(ctx, from, to, false, req.Msg.InboundNumbers.GetNumbers())

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
		Customer(req.Msg.Id)

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

func (svc *CallService) GetUserIdForAgent(ctx context.Context, agent string) string {
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
