package config

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tierklinik-dobersberg/3cx-support/internal/database"
	"github.com/tierklinik-dobersberg/3cx-support/internal/oncalloverwrite"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/idm/v1/idmv1connect"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/roster/v1/rosterv1connect"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Providers struct {
	Roster rosterv1connect.RosterServiceClient
	Users  idmv1connect.UserServiceClient

	CallLogDB   database.Database
	OverwriteDB oncalloverwrite.Database

	Config Config
}

func NewProviders(ctx context.Context, cfg Config) (*Providers, error) {
	httpClient := http.DefaultClient

	cli, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURL))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to mongodb: %w", err)
	}

	// try to ping mongo
	if err := cli.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to ping mongodb: %w", err)
	}

	callogDB, err := database.New(ctx, cfg.Database, cfg.Country, cli)
	if err != nil {
		return nil, fmt.Errorf("failed to perpare calllog db: %w", err)
	}

	overwriteDB, err := oncalloverwrite.New(ctx, cfg.Database, cli)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare overwrite db: %w", err)
	}

	p := &Providers{
		Roster:      rosterv1connect.NewRosterServiceClient(httpClient, cfg.RosterdURL),
		Users:       idmv1connect.NewUserServiceClient(httpClient, cfg.IdmURL),
		Config:      cfg,
		CallLogDB:   callogDB,
		OverwriteDB: overwriteDB,
	}

	return p, nil
}
