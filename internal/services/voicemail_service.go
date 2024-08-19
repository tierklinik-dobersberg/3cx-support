package services

import (
	"context"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/3cx-support/internal/voicemail"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1/pbx3cxv1connect"
)

type VoiceMailService struct {
	pbx3cxv1connect.UnimplementedVoiceMailServiceHandler

	providers *config.Providers
	manager   *voicemail.Manager
}

func NewVoiceMailService(ctx context.Context, providers *config.Providers) (*VoiceMailService, error) {
	mng, err := voicemail.NewManager(ctx, providers)
	if err != nil {
		return nil, err
	}

	svc := &VoiceMailService{
		providers: providers,
		manager:   mng,
	}

	return svc, nil
}

func (svc *VoiceMailService) CreateMailbox(ctx context.Context, req *connect.Request[pbx3cxv1.CreateMailboxRequest]) (*connect.Response[pbx3cxv1.CreateMailboxResponse], error) {
	if err := svc.manager.CreateMailbox(ctx, req.Msg.Mailbox); err != nil {
		return nil, err
	}

	return connect.NewResponse(&pbx3cxv1.CreateMailboxResponse{}), nil
}

func (svc *VoiceMailService) ListMailboxes(ctx context.Context, req *connect.Request[pbx3cxv1.ListMailboxesRequest]) (*connect.Response[pbx3cxv1.ListMailboxesResponse], error) {
	boxes, err := svc.providers.MailboxDatabase.ListMailboxes(ctx)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pbx3cxv1.ListMailboxesResponse{
		Mailboxes: boxes,
	}), nil
}

func (svc *VoiceMailService) ListVoiceMails(ctx context.Context, req *connect.Request[pbx3cxv1.ListVoiceMailsRequest]) (*connect.Response[pbx3cxv1.ListVoiceMailsResponse], error) {
	res, err := svc.providers.MailboxDatabase.ListVoiceMails(ctx, req.Msg.Mailbox, req.Msg.Filter)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&pbx3cxv1.ListVoiceMailsResponse{
		Voicemails: res,
	}), nil
}

func (svc *VoiceMailService) MarkVoiceMails(ctx context.Context, req *connect.Request[pbx3cxv1.MarkVoiceMailsRequest]) (*connect.Response[pbx3cxv1.MarkVoiceMailsResponse], error) {
	if err := svc.providers.MailboxDatabase.MarkVoiceMails(ctx, req.Msg.Seen, req.Msg.GetVoicemailIds()); err != nil {
		return nil, err
	}

	return connect.NewResponse(&pbx3cxv1.MarkVoiceMailsResponse{}), nil
}
