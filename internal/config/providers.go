package config

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tierklinik-dobersberg/3cx-support/internal/database"
	"github.com/tierklinik-dobersberg/3cx-support/internal/oncalloverwrite"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1/customerv1connect"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/idm/v1/idmv1connect"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/roster/v1/rosterv1connect"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Providers struct {
	Roster   rosterv1connect.RosterServiceClient
	Users    idmv1connect.UserServiceClient
	Notify   idmv1connect.NotifyServiceClient
	Roles    idmv1connect.RoleServiceClient
	Customer customerv1connect.CustomerServiceClient

	CallLogDB   database.Database
	OverwriteDB oncalloverwrite.Database

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

	p := &Providers{
		Roster:      rosterv1connect.NewRosterServiceClient(httpClient, cfg.RosterdURL),
		Users:       idmv1connect.NewUserServiceClient(httpClient, cfg.IdmURL),
		Notify:      idmv1connect.NewNotifyServiceClient(httpClient, cfg.IdmURL),
		Roles:       idmv1connect.NewRoleServiceClient(httpClient, cfg.IdmURL),
		Customer:    customerv1connect.NewCustomerServiceClient(cli.NewInsecureHttp2Client(), cfg.CustomerServiceURL),
		Config:      cfg,
		CallLogDB:   callogDB,
		OverwriteDB: overwriteDB,
	}

	return p, nil
}
