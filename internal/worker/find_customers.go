package worker

import (
	"context"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/apis/pkg/log"

	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
)

func StartFindCustomerWorker(ctx context.Context, providers *config.Providers) {
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
}
