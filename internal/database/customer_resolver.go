package database

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1/customerv1connect"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"golang.org/x/sync/singleflight"
)

// CustomerResolver provides the ability to resolve customer records from the
// tkd/customer-service by evaluating a SearchQuery on call-logs. It aggregates
// a distinct set of customer IDs and then fetches the customer records from the
// service.
type CustomerResolver struct {
	db  Database
	cli customerv1connect.CustomerServiceClient

	inflight *singleflight.Group

	customerLock sync.Mutex
	customers    map[string]*customerv1.Customer

	recordsLock sync.Mutex
	records     []structs.CallLog
}

func NewCustomerResolver(db Database, cli customerv1connect.CustomerServiceClient) *CustomerResolver {
	return &CustomerResolver{
		db:        db,
		cli:       cli,
		inflight:  &singleflight.Group{},
		customers: make(map[string]*customerv1.Customer),
	}
}

func (cr *CustomerResolver) Query(ctx context.Context, query *SearchQuery) ([]*pbx3cxv1.CallEntry, []*customerv1.Customer, error) {

	errs := new(multierror.Error)

	// this one cancels as soon as the h2 stream ends

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultChan, errChan := cr.db.StreamSearch(ctx, query)
	stream := cr.cli.SearchCustomerStream(ctx)

	go func() {
	L:
		for {
			select {
			case <-ctx.Done():
				log.L(ctx).Debug("request context cancelled")

				break L

			case record, ok := <-resultChan:
				if !ok {
					log.L(ctx).Debug("result channel closed")

					break L
				}

				// first, append the record to the result list
				cr.recordsLock.Lock()
				cr.records = append(cr.records, record)
				cr.recordsLock.Unlock()

				// next, check if we need to fetch a customer record
				if record.CustomerID != "" {
					cr.customerLock.Lock()
					_, ok := cr.customers[record.CustomerID]
					cr.customerLock.Unlock()

					if !ok {
						// search for the customer
						_, err, _ := cr.inflight.Do(record.CustomerID, func() (interface{}, error) {
							cr.customerLock.Lock()
							cr.customers[record.CustomerID] = nil
							cr.customerLock.Unlock()

							log.L(ctx).Debug("sending customer query", "customerSource", record.CustomerSource, "customerId", record.CustomerID)

							if record.CustomerSource == "" {
								if err := stream.Send(&customerv1.SearchCustomerRequest{
									Queries: []*customerv1.CustomerQuery{
										{
											Query: &customerv1.CustomerQuery_Id{
												Id: record.CustomerID,
											},
										},
									},
								}); err != nil {
									return nil, fmt.Errorf("failed to send query: %w", err)
								}
							} else {
								if err := stream.Send(&customerv1.SearchCustomerRequest{
									Queries: []*customerv1.CustomerQuery{
										{
											Query: &customerv1.CustomerQuery_InternalReference{
												InternalReference: &customerv1.InternalReferenceQuery{
													Importer: record.CustomerSource,
													Ref:      record.CustomerID,
												},
											},
										},
									},
								}); err != nil {
									return nil, fmt.Errorf("failed to send query: %w", err)
								}
							}

							return nil, nil
						})
						if err != nil {
							log.L(ctx).Error("failed to send customer lookup query", "customerSource", record.CustomerSource, "customerId", record.CustomerID, "error", err)

							cancel()
						}
					}
				}

			case err, ok := <-errChan:
				if !ok {
					log.L(ctx).Debug("error channel closed")

					break L
				}

				errs.Errors = append(errs.Errors, err)
			}
		}

		if err := stream.CloseRequest(); err != nil {
			log.L(ctx).Error("failed to close request side of stream", "error", err)
		} else {
			log.L(ctx).Debug("send side closed succesfully")
		}
	}()

	for {
		msg, err := stream.Receive()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.L(ctx).Error("failed to receive message from stream", "error", err)
			}

			break
		}

		cr.customerLock.Lock()
		for _, c := range msg.Results {
			cr.customers[c.Customer.Id] = c.Customer
		}
		cr.customerLock.Unlock()

		for _, c := range msg.Results {
			log.L(ctx).Debug("received customer lookup response", "firstName", c.Customer.FirstName, "lastName", c.Customer.LastName, "customerId", c.Customer.Id)
		}
	}

	log.L(ctx).Debug("search stream completed")

	cr.customerLock.Lock()
	defer cr.customerLock.Unlock()

	cr.recordsLock.Lock()
	defer cr.recordsLock.Unlock()

	log.L(ctx).Debug("prepareing search result")

	results := make([]*pbx3cxv1.CallEntry, len(cr.records))
	for idx, r := range cr.records {
		results[idx] = r.ToProto()
	}

	customers := make([]*customerv1.Customer, 0, len(cr.customers))
	for _, c := range cr.customers {
		if c != nil {
			customers = append(customers, c)
		}
	}

	return results, customers, errs.ErrorOrNil()
}
