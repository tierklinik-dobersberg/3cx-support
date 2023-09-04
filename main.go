package main

import (
	"context"
	"net/http"
	"os"

	"github.com/bufbuild/connect-go"
	"github.com/bufbuild/protovalidate-go"
	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/3cx-support/internal/services"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1/pbx3cxv1connect"
	"github.com/tierklinik-dobersberg/apis/pkg/cors"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"github.com/tierklinik-dobersberg/apis/pkg/server"
	"github.com/tierklinik-dobersberg/apis/pkg/validator"
)

func main() {
	ctx := context.Background()

	var cfgFilePath string
	if len(os.Args) > 1 {
		cfgFilePath = os.Args[1]
	}

	cfg, err := config.LoadConfig(ctx, cfgFilePath)
	if err != nil {
		logrus.Fatalf("failed to load configuration: %w", err)
	}

	providers, err := config.NewProviders(ctx, *cfg)
	if err != nil {
		logrus.Fatalf("failed to prepare providers: %w", err)
	}

	protoValidator, err := protovalidate.New()
	if err != nil {
		logrus.Fatalf("failed to prepare protovalidator: %w", err)
	}

	interceptors := connect.WithInterceptors(
		log.NewLoggingInterceptor(),
		validator.NewInterceptor(protoValidator),
	)

	corsConfig := cors.Config{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowCredentials: true,
	}

	// Prepare our servemux and add handlers.
	serveMux := http.NewServeMux()

	// create a new CallService and add it to the mux.
	callService := services.New(providers)
	serveMux.Handle("api/external/v1/calllog", http.HandlerFunc(callService.RecordCallHandler))

	path, handler := pbx3cxv1connect.NewCallServiceHandler(callService, interceptors)
	serveMux.Handle(path, handler)

	// Create the server
	srv := server.Create(cfg.ListenAddress, cors.Wrap(corsConfig, serveMux))

	if err := server.Serve(ctx, srv); err != nil {
		logrus.Fatalf("failed to serve: %w", err)
	}
}
