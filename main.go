package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/bufbuild/protovalidate-go"
	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/3cx-support/internal/services"
	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
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

	// TODO(ppacher): privacy-interceptor

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

	voiceMailSerivce, err := services.NewVoiceMailService(ctx, providers)
	if err != nil {
		logrus.Fatalf("failed to prepare voicemail service: %s", err.Error())
	}

	path, handler = pbx3cxv1connect.NewVoiceMailServiceHandler(voiceMailSerivce, interceptors)
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

	ticker := time.NewTicker(time.Minute * 10)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)

		func() {
			defer cancel()
			res, err := providers.CallLogDB.FindDistinctNumbersWithoutCustomers(ctx)
			if err != nil {
				log.L(ctx).Errorf("failed to find distinct, unidentified numbers: %s", err)
				return
			}

			log.L(ctx).Infof("found %d distinct numbers that are not associated with a customer record", len(res))

			queries := make([]*customerv1.CustomerQuery, len(res))

			for idx, r := range res {
				queries[idx] = &customerv1.CustomerQuery{
					Query: &customerv1.CustomerQuery_PhoneNumber{
						PhoneNumber: r,
					},
				}
			}

			queryResult, err := providers.Customer.SearchCustomer(ctx, connect.NewRequest(&customerv1.SearchCustomerRequest{
				Queries: queries,
			}))
			if err != nil {
				log.L(ctx).Errorf("failed to search for customers: %s", err)
			} else {
				log.L(ctx).Infof("found %d customers for unmatched numbers", len(queryResult.Msg.Results))

				for _, c := range queryResult.Msg.Results {
					for _, number := range c.Customer.PhoneNumbers {
						if err := providers.CallLogDB.UpdateUnmatchedNumber(ctx, number, c.Customer.Id); err != nil {
							log.L(ctx).Errorf("failed to update unmatched customers for %s (phone=%q): %s", c.Customer.Id, number, err.Error())
						}
					}
				}
			}
		}()

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}()

	if err := server.Serve(ctx, srv); err != nil {
		logrus.Fatalf("failed to serve: %s", err)
	}
}
