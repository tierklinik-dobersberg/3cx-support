package services

import (
	"context"
	"errors"

	"github.com/bufbuild/connect-go"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"go.mongodb.org/mongo-driver/mongo"
)

func (svc *CallService) CreateInboundNumber(ctx context.Context, req *connect.Request[pbx3cxv1.CreateInboundNumberRequest]) (*connect.Response[pbx3cxv1.CreateInboundNumberResponse], error) {
	err := svc.OverwriteDB.CreateInboundNumber(ctx, req.Msg.Number, req.Msg.DisplayName)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pbx3cxv1.CreateInboundNumberResponse{
		InboundNumber: &pbx3cxv1.InboundNumber{
			Number:      req.Msg.Number,
			DisplayName: req.Msg.DisplayName,
		},
	}), nil
}

func (svc *CallService) DeleteInboundNumber(ctx context.Context, req *connect.Request[pbx3cxv1.DeleteInboundNumberRequest]) (*connect.Response[pbx3cxv1.DeleteInboundNumberResponse], error) {
	err := svc.OverwriteDB.DeleteInboundNumber(ctx, req.Msg.Number)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pbx3cxv1.DeleteInboundNumberResponse{}), nil
}

func (svc *CallService) UpdateInboundNumber(ctx context.Context, req *connect.Request[pbx3cxv1.UpdateInboundNumberRequest]) (*connect.Response[pbx3cxv1.UpdateInboundNumberResponse], error) {
	err := svc.OverwriteDB.UpdateInboundNumber(ctx, req.Msg.Number, req.Msg.NewDisplayName)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pbx3cxv1.UpdateInboundNumberResponse{
		InboundNumber: &pbx3cxv1.InboundNumber{
			Number:      req.Msg.Number,
			DisplayName: req.Msg.NewDisplayName,
		},
	}), nil
}

func (svc *CallService) ListInboundNumbers(ctx context.Context, req *connect.Request[pbx3cxv1.ListInboundNumberRequest]) (*connect.Response[pbx3cxv1.ListInboundNumberResponse], error) {
	res, err := svc.OverwriteDB.ListInboundNumbers(ctx)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return connect.NewResponse(&pbx3cxv1.ListInboundNumberResponse{}), nil
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	response := &pbx3cxv1.ListInboundNumberResponse{
		InboundNumbers: make([]*pbx3cxv1.InboundNumber, len(res)),
	}

	for idx, r := range res {
		response.InboundNumbers[idx] = r.ToProto()
	}

	return connect.NewResponse(response), nil
}
