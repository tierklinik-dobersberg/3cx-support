package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/structs"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"go.mongodb.org/mongo-driver/mongo"
)

func (svc *CallService) CreateInboundNumber(ctx context.Context, req *connect.Request[pbx3cxv1.CreateInboundNumberRequest]) (*connect.Response[pbx3cxv1.CreateInboundNumberResponse], error) {
	model := structs.InboundNumber{
		Number:          req.Msg.Number,
		DisplayName:     req.Msg.DisplayName,
		RosterTypeName:  req.Msg.RosterTypeName,
		RosterShiftTags: req.Msg.RosterShiftTags,
		ResultLimit:     int(req.Msg.ResultLimit),
	}

	err := svc.OverwriteDB.CreateInboundNumber(ctx, model)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pbx3cxv1.CreateInboundNumberResponse{
		InboundNumber: model.ToProto(),
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
	model, err := svc.OverwriteDB.GetInboundNumber(ctx, req.Msg.Number)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}

		return nil, err
	}

	paths := []string{
		"display_name",
		"roster_shift_tags",
		"roster_type_name",
	}

	if pb := req.Msg.UpdateMask.GetPaths(); len(pb) > 0 {
		paths = pb
	}

	for _, p := range paths {
		switch p {
		case "display_name":
			model.DisplayName = req.Msg.NewDisplayName

		case "roster_shift_tags":
			model.RosterShiftTags = req.Msg.RosterShiftTags

		case "roster_type_name":
			model.RosterTypeName = req.Msg.RosterTypeName

		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid path in update_mask: %q", p))
		}
	}

	if err := svc.OverwriteDB.UpdateInboundNumber(ctx, model); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pbx3cxv1.UpdateInboundNumberResponse{
		InboundNumber: model.ToProto(),
	}), nil
}

func (svc *CallService) ListInboundNumber(ctx context.Context, req *connect.Request[pbx3cxv1.ListInboundNumberRequest]) (*connect.Response[pbx3cxv1.ListInboundNumberResponse], error) {
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
