package services

import (
	"context"
	"fmt"

	"github.com/bufbuild/connect-go"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"google.golang.org/protobuf/types/known/emptypb"
)

func (svc *CallService) ListPhoneExtensions(ctx context.Context, req *connect.Request[pbx3cxv1.ListPhoneExtensionsRequest]) (*connect.Response[pbx3cxv1.ListPhoneExtensionsResponse], error) {
	extensions, err := svc.Extensions.ListPhoneExtensions(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pbx3cxv1.ListPhoneExtensionsResponse{
		PhoneExtensions: extensions,
	}), nil
}

func (svc *CallService) RegisterPhoneExtension(ctx context.Context, req *connect.Request[pbx3cxv1.RegisterPhoneExtensionRequest]) (*connect.Response[pbx3cxv1.PhoneExtension], error) {
	// TODO(ppacher): verify the extension does not yet exist.
	if err := svc.Extensions.SavePhoneExtension(ctx, req.Msg.PhoneExtension); err != nil {
		return nil, err
	}

	return connect.NewResponse(req.Msg.PhoneExtension), nil
}

func (svc *CallService) DeletePhoneExtension(ctx context.Context, req *connect.Request[pbx3cxv1.DeletePhoneExtensionRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := svc.Extensions.DeletePhoneExtension(ctx, req.Msg.Extension); err != nil {
		return nil, err
	}

	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (svc *CallService) UpdatePhoneExtension(ctx context.Context, req *connect.Request[pbx3cxv1.UpdatePhoneExtensionRequest]) (*connect.Response[pbx3cxv1.PhoneExtension], error) {
	exts, err := svc.Extensions.ListPhoneExtensions(ctx)
	if err != nil {
		return nil, err
	}

	var ext *pbx3cxv1.PhoneExtension
	for _, e := range exts {
		if e.Extension == req.Msg.Extension {
			ext = e
			break
		}
	}

	if ext == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("phone extension does not exist"))
	}

	paths := []string{"extension", "display_name", "eligible_for_overwrite", "internal_queue"}
	if fm := req.Msg.GetUpdateMask().GetPaths(); len(fm) > 0 {
		paths = fm
	}

	if req.Msg.PhoneExtension == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("phone_extension must not be nil"))
	}

	for _, p := range paths {
		switch p {
		case "extension":
			ext.Extension = req.Msg.PhoneExtension.Extension
			if ext.Extension == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("extension must not be empty"))
			}

		case "display_name":
			ext.DisplayName = req.Msg.PhoneExtension.DisplayName
			if ext.DisplayName == "" {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("display_name must not be empty"))
			}

		case "eligible_for_overwrite":
			ext.EligibleForOverwrite = req.Msg.PhoneExtension.EligibleForOverwrite

		case "internal_queue":
			ext.InternalQueue = req.Msg.PhoneExtension.InternalQueue

		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid field name %q in update_mask", p))
		}
	}

	if err := svc.Extensions.UpdatePhoneExtension(ctx, req.Msg.Extension, ext); err != nil {
		return nil, err
	}

	return connect.NewResponse(ext), nil
}
