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
	"github.com/tierklinik-dobersberg/apis/pkg/auth"
	"github.com/tierklinik-dobersberg/apis/pkg/cors"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"github.com/tierklinik-dobersberg/apis/pkg/server"
	"github.com/tierklinik-dobersberg/apis/pkg/validator"
	"google.golang.org/protobuf/reflect/protoregistry"
)

func main() {
	ctx := context.Background()

	var cfgFilePath string
	if len(os.Args) > 1 {
		cfgFilePath = os.Args[1]
	}

	cfg, err := config.LoadConfig(ctx, cfgFilePath)
	if err != nil {
		logrus.Fatalf("failed to load configuration: %s", err)
	}
	logrus.Infof("configuration loaded successfully")

	providers, err := config.NewProviders(ctx, *cfg)
	if err != nil {
		logrus.Fatalf("failed to prepare providers: %s", err)
	}
	logrus.Infof("application providers prepared successfully")

	protoValidator, err := protovalidate.New()
	if err != nil {
		logrus.Fatalf("failed to prepare protovalidator: %s", err)
	}

	authInterceptor := auth.NewAuthAnnotationInterceptor(
		protoregistry.GlobalFiles,
		auth.NewIDMRoleResolver(providers.Roles),
		auth.RemoteHeaderExtractor)

	interceptors := connect.WithInterceptors(
		log.NewLoggingInterceptor(),
		authInterceptor,
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
	serveMux.Handle("/api/external/v1/calllog", http.HandlerFunc(callService.RecordCallHandler))

	path, handler := pbx3cxv1connect.NewCallServiceHandler(callService, interceptors)
	serveMux.Handle(path, handler)

	loggingHandler := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logrus.Infof("received request: %s %s%s", r.Method, r.Host, r.URL.String())

			next.ServeHTTP(w, r)
		})
	}

	// Create the server
	srv, err := server.CreateWithOptions(cfg.ListenAddress, loggingHandler(serveMux), server.WithCORS(corsConfig))
	if err != nil {
		logrus.Fatalf("failed to setup server: %s", err)
	}

	logrus.Infof("HTTP/2 server (h2c) prepared successfully, startin to listen ...")

	if err := server.Serve(ctx, srv); err != nil {
		logrus.Fatalf("failed to serve: %s", err)
	}
}
