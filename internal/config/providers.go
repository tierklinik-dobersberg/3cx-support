package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	connect "github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/internal/database"
	"github.com/tierklinik-dobersberg/3cx-support/internal/oncalloverwrite"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1/customerv1connect"
	eventsv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/events/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/events/v1/eventsv1connect"
	idmv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/idm/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/idm/v1/idmv1connect"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	rosterv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/roster/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/roster/v1/rosterv1connect"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Providers struct {
	Roster   rosterv1connect.RosterServiceClient
	Users    idmv1connect.UserServiceClient
	Notify   idmv1connect.NotifyServiceClient
	Roles    idmv1connect.RoleServiceClient
	Customer customerv1connect.CustomerServiceClient
	Events   eventsv1connect.EventServiceClient

	CallLogDB       database.Database
	OverwriteDB     oncalloverwrite.Database
	MailboxDatabase database.MailboxDatabase
	Extensions      database.ExtensionDatabase

	Config Config
}

func NewProviders(ctx context.Context, cfg Config) (*Providers, error) {
	httpClient := http.DefaultClient

	mongoCli, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURL))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongodb: %w", err)
	}

	// try to ping mongo
	if err := mongoCli.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping mongodb: %w", err)
	}

	callogDB, err := database.New(ctx, cfg.Database, cfg.Country, mongoCli)
	if err != nil {
		return nil, fmt.Errorf("failed to perpare calllog db: %w", err)
	}

	overwriteDB, err := oncalloverwrite.New(ctx, cfg.Database, mongoCli)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare overwrite db: %w", err)
	}

	mailboxDB, err := database.NewMailboxDatabase(ctx, mongoCli.Database(cfg.Database))
	if err != nil {
		return nil, fmt.Errorf("failed to prepare mailbox db: %w", err)
	}

	extDB, err := database.NewExtensionDatabase(ctx, mongoCli.Database(cfg.Database))
	if err != nil {
		return nil, fmt.Errorf("failed to create phone-extension database: %w", err)
	}

	p := &Providers{
		Roster:          rosterv1connect.NewRosterServiceClient(httpClient, cfg.RosterdURL),
		Users:           idmv1connect.NewUserServiceClient(httpClient, cfg.IdmURL),
		Notify:          idmv1connect.NewNotifyServiceClient(httpClient, cfg.IdmURL),
		Roles:           idmv1connect.NewRoleServiceClient(httpClient, cfg.IdmURL),
		Customer:        customerv1connect.NewCustomerServiceClient(cli.NewInsecureHttp2Client(), cfg.CustomerServiceURL),
		Config:          cfg,
		CallLogDB:       callogDB,
		OverwriteDB:     overwriteDB,
		MailboxDatabase: mailboxDB,
		Extensions:      extDB,
	}

	return p, nil
}

func (svc *Providers) ResolveOnCallTarget(ctx context.Context, dateTime time.Time, ignoreOverwrites bool, inboundNumber string) (*pbx3cxv1.GetOnCallResponse, error) {
	var numbers []string

	if inboundNumber == "" && svc.Config.DefaultOnCallInboundNumber != "" {
		inboundNumber = svc.Config.DefaultOnCallInboundNumber
	}

	if inboundNumber != "" {
		numbers = []string{inboundNumber}
	}

	overwrite, err := svc.OverwriteDB.GetActiveOverwrite(ctx, dateTime, numbers)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("database: %w", err)
	}

	if overwrite != nil && !ignoreOverwrites {
		target, profile, err := svc.ResolveOverwriteTarget(ctx, *overwrite)
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

		return res, nil
	}

	var inboundNumberModel structs.InboundNumber

	if inboundNumber != "" {
		var err error
		inboundNumberModel, err = svc.OverwriteDB.GetInboundNumber(ctx, inboundNumber)
		if err != nil {
			log.L(ctx).Errorf("failed to get inbound number model for %q, using default: %s", inboundNumber, err)
		}
	}

	if inboundNumberModel.RosterTypeName == "" {
		inboundNumberModel.RosterTypeName = svc.Config.RosterTypeName
	}

	workingStaff, err := svc.Roster.GetWorkingStaff2(ctx, connect.NewRequest(&rosterv1.GetWorkingStaffRequest2{
		Query: &rosterv1.GetWorkingStaffRequest2_Time{
			Time: timestamppb.New(dateTime),
		},
		RosterTypeName: inboundNumberModel.RosterTypeName,
		ShiftTags:      inboundNumberModel.RosterShiftTags,
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
		if inboundNumberModel.ResultLimit > 0 && len(res.OnCall) >= inboundNumberModel.ResultLimit {
			break
		}

		profile, err := svc.FetchUserProfile(ctx, userId)
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

		target := svc.GetUserTransferTarget(profile)
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

	return res, nil
}

func (svc *Providers) ResolveOverwriteTarget(ctx context.Context, overwrite structs.Overwrite) (string, *idmv1.Profile, error) {
	target := overwrite.PhoneNumber
	var profile *idmv1.Profile

	if overwrite.UserID != "" {
		var err error
		profile, err = svc.FetchUserProfile(ctx, overwrite.UserID)
		if err != nil {
			return "", nil, fmt.Errorf("failed to fetch user with id %q: %w", overwrite.UserID, err)
		}

		target = svc.GetUserTransferTarget(profile)
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

func (svc *Providers) FetchUserProfile(ctx context.Context, userId string) (*idmv1.Profile, error) {
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

func (svc *Providers) GetUserTransferTarget(profile *idmv1.Profile) string {
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

func (svc *Providers) PublishEvent(event proto.Message, retained bool) {
	go func() {
		pb, err := anypb.New(event)
		if err != nil {
			slog.Error("failed to convert protobuf message to anypb.Any", "error", err, "messageType", proto.MessageName(event))
			return
		}

		evt := &eventsv1.Event{
			Event:    pb,
			Retained: retained,
		}

		if _, err := svc.Events.Publish(context.Background(), connect.NewRequest(evt)); err != nil {
			slog.Error("failed to publish event", "error", err, "messageType", proto.MessageName(event))
		}
	}()
}
