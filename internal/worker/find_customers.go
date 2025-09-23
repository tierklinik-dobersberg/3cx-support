package worker

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/apis/pkg/log"

	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
)

func StartFindCustomerWorker(ctx context.Context, providers *config.Providers) {
	go func() {
		ticker := time.NewTicker(time.Minute * 10)
		defer ticker.Stop()

		for {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute*5)

			l := log.L(ctx)

			func() {
				defer cancel()
				res, err := providers.CallLogDB.FindDistinctNumbersWithoutCustomers(ctx)
				if err != nil {
					l.Error("failed to find distinct, unidentified numbers", "error", err)
					return
				}

				res2, err := providers.MailboxDatabase.FindDistinctNumbersWithoutCustomers(ctx)
				if err != nil {
					l.Error("failed to find distinct, unidentified numbers in voicemails", "error", err)
				}

				res = append(res, res2...)
				sort.Stable(sort.StringSlice(res))
				slices.Compact(res)

				l.Info("found distinct numbers that are not associated with a customer record", "count", len(res))

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
					l.Error("failed to search for customers", "error", err)
				} else {
					l.Info("found customers for unmatched numbers", "count", len(queryResult.Msg.Results))

					for _, c := range queryResult.Msg.Results {
						for _, number := range c.Customer.PhoneNumbers {
							if err := providers.CallLogDB.UpdateUnmatchedNumber(ctx, number, c.Customer.Id); err != nil {
								l.Error("failed to update unmatched customers", "customerId", c.Customer.Id, "phoneNumber", number, "error", err.Error())
							}

							if err := providers.MailboxDatabase.UpdateUnmatchedNumber(ctx, number, c.Customer.Id); err != nil {
								l.Error("failed to update unmatched customers", "customerId", c.Customer.Id, "phoneNumber", number, "error", err.Error())
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
		}
	}()
}
