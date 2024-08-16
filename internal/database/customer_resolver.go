package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1/customerv1connect"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/log"
	"golang.org/x/sync/singleflight"
)

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

	var wg sync.WaitGroup
	errs := new(multierror.Error)

	// this one cancels as soon as the h2 stream ends
	resultChan, errChan := cr.db.StreamSearch(ctx, query)

	stream := cr.cli.SearchCustomerStream(ctx)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		defer log.L(ctx).Infof("receive loop finished")

		for {
			msg, err := stream.Receive()
			if err != nil {
				log.L(ctx).Errorf("failed to receive message: %s", err)

				return
			}

			// for each sent request, we add 1 to the waitgroup
			wg.Done()

			log.L(ctx).Infof("waiting for customer lock")
			cr.customerLock.Lock()
			for _, c := range msg.Results {
				cr.customers[c.Customer.Id] = c.Customer
			}
			cr.customerLock.Unlock()
			log.L(ctx).Infof("customer map updated")

			for _, c := range msg.Results {
				log.L(ctx).Infof("received customer response %s %s (%s)", c.Customer.FirstName, c.Customer.LastName, c.Customer.Id)
			}
		}
	}()

L:
	for {
		select {
		case <-ctx.Done():
			log.L(ctx).Infof("request context cancelled")

			break L

		case record, ok := <-resultChan:
			if !ok {
				log.L(ctx).Infof("result channel closed")

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

						logrus.Infof("sending customer query for %s/%s", record.CustomerSource, record.CustomerID)
						wg.Add(1)

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
								wg.Done()
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
								wg.Done()
								return nil, fmt.Errorf("failed to send query: %w", err)
							}
						}

						return nil, nil
					})
					if err != nil {
						log.L(ctx).Errorf("failed to send customer lookup query: %s", err)

						cancel()
					}
				}
			}

		case err, ok := <-errChan:
			if !ok {
				log.L(ctx).Infof("error channel closed")

				break L
			}

			errs.Errors = append(errs.Errors, err)
		}
	}

	if err := stream.CloseRequest(); err != nil {
		log.L(ctx).Errorf("failed to close request side of stream: %s", err)
	}

	log.L(ctx).Infof("waiting for goroutines to finish")

	go func() {
		time.Sleep(time.Second * 30)
		cancel()
	}()

	wg.Wait()

	log.L(ctx).Infof("waiting for customer and record locks")
	cr.customerLock.Lock()
	defer cr.customerLock.Unlock()

	cr.recordsLock.Lock()
	defer cr.recordsLock.Unlock()

	log.L(ctx).Infof("prepareing search result")

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
