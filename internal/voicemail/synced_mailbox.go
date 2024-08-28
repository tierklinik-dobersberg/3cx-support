package voicemail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/database"
	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1/customerv1connect"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/mailsync"
	"github.com/tierklinik-dobersberg/mailbox"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Mailbox handles voicmails.
type Mailbox struct {
	syncer       *mailsync.Syncer
	callerRegexp *regexp.Regexp
	targetRegexp *regexp.Regexp
	name         string
	storagePath  string
	database     database.MailboxDatabase
	customerCli  customerv1connect.CustomerServiceClient
}

func NewMailboxSyncer(
	ctx context.Context,
	storagePath string,
	mng *mailsync.Manager,
	database database.MailboxDatabase,
	customerCli customerv1connect.CustomerServiceClient,
	mb *pbx3cxv1.Mailbox,
) (*Mailbox, error) {

	cfg := mb.GetConfig()
	syncer, err := mng.NewSyncer(ctx, mb.Id, mb.PollInterval.AsDuration(), &mailbox.Config{
		Host:               cfg.GetHost(),
		TLS:                cfg.GetTls(),
		InsecureSkipVerify: cfg.GetInsecureSkipVerify(),
		User:               cfg.GetUser(),
		Password:           cfg.GetPassword(),
		Folder:             cfg.GetFolder(),
		ReadOnly:           cfg.GetReadOnly(),
	})
	if err != nil {
		return nil, err
	}

	box := &Mailbox{
		syncer:      syncer,
		name:        mb.Id,
		database:    database,
		storagePath: storagePath,
		customerCli: customerCli,
	}
	syncer.OnMessage(box)

	if mb.GetExtractCallerRegexp() != "" {
		box.callerRegexp, err = regexp.Compile(mb.ExtractCallerRegexp)
		if err != nil {
			return nil, fmt.Errorf("invalid caller regexp: %w", err)
		}
	}

	if mb.GetExtractTargetRegexp() != "" {
		box.targetRegexp, err = regexp.Compile(mb.ExtractTargetRegexp)
		if err != nil {
			return nil, fmt.Errorf("invalid target regexp: %w", err)
		}
	}

	if err := syncer.Start(); err != nil {
		return nil, err
	}

	return box, nil
}

func (box *Mailbox) Dispose() error {
	return box.syncer.Stop()
}

func (box *Mailbox) getCustomer(ctx context.Context, caller string) (*customerv1.Customer, error) {
	res, err := box.customerCli.SearchCustomer(ctx, connect.NewRequest(&customerv1.SearchCustomerRequest{
		Queries: []*customerv1.CustomerQuery{
			{
				Query: &customerv1.CustomerQuery_PhoneNumber{
					PhoneNumber: caller,
				},
			},
		},
	}))

	if err != nil {
		if cerr, ok := err.(*connect.Error); ok && cerr.Code() == connect.CodeNotFound {
			return nil, nil
		}

		return nil, err
	}

	if len(res.Msg.Results) > 0 {
		return res.Msg.Results[0].Customer, nil
	}

	return nil, nil
}

// trunk-ignore(golangci-lint/cyclop)
func (box *Mailbox) extractData(_ context.Context, mail *mailbox.EMail) (caller, target, body string) {
	texts := mail.FindByMIME("text/plain")
	if len(texts) == 0 {
		texts = mail.FindByMIME("text/html")
	}

	for _, part := range texts {
		body += string(part.Body)

		if caller == "" && box.callerRegexp != nil {
			matches := box.callerRegexp.FindStringSubmatch(string(part.Body))
			if len(matches) >= 2 {
				caller = matches[1]
			}
		}

		if target == "" && box.targetRegexp != nil {
			matches := box.targetRegexp.FindStringSubmatch(string(part.Body))
			if len(matches) >= 2 {
				target = matches[1]
			}
		}

		if target != "" && caller != "" {
			return caller, target, body
		}
	}

	return caller, target, body
}

func (box *Mailbox) saveVoiceAttachment(ctx context.Context, targetDir, caller string, mail *mailbox.EMail) (string, error) {
	var voiceFiles = mail.FindByMIME("application/octet-stream")
	if len(voiceFiles) == 0 {
		return "", fmt.Errorf("no voice recordings found")
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		slog.Error("failed to create target directory", slog.Any("directory", targetDir), slog.Any("error", err.Error()))
		// continue for now, saving the file might still succeed
	}

	fileName := fmt.Sprintf(
		"%s-%s%s",
		time.Now().Format(time.RFC3339),
		caller,
		filepath.Ext(voiceFiles[0].FileName),
	)
	path := filepath.Join(
		targetDir,
		fileName,
	)
	targetFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to create voice file at %s: %w", path, err)
	}

	hasher := sha256.New()
	multiwriter := io.MultiWriter(hasher, targetFile)

	if _, err := multiwriter.Write(voiceFiles[0].Body); err != nil {
		targetFile.Close()

		return "", fmt.Errorf("failed to create voice file at %s: %w", path, err)
	}

	if err := targetFile.Close(); err != nil {
		slog.Error("failed to close voice file", slog.Any("path", path), slog.Any("error", err.Error()))
	}

	hash := hex.EncodeToString(hasher.Sum(nil))
	newPath := filepath.Join(
		targetDir,
		fmt.Sprintf("%s%s", hash, filepath.Ext(voiceFiles[0].FileName)),
	)

	if err := os.Rename(path, newPath); err != nil {
		return "", fmt.Errorf("failed to rename voice file from %s to %s: %w", path, newPath, err)
	}

	return newPath, nil
}

// HandleMail implements mailsync.MessageHandler.
func (box *Mailbox) HandleMail(ctx context.Context, mail *mailbox.EMail) {
	caller, target, body := box.extractData(ctx, mail)

	customer, err := box.getCustomer(ctx, caller)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get customer", slog.Any("error", err.Error()), slog.Any("caller", caller))
	}

	filePath, err := box.saveVoiceAttachment(ctx, box.storagePath, caller, mail)
	if err != nil {
		slog.ErrorContext(ctx, "failed to save voicemail attachment", slog.Any("caller", caller), slog.Any("error", err.Error()))

		return
	}

	record := &pbx3cxv1.VoiceMail{
		Mailbox:       box.name,
		ReceiveTime:   timestamppb.New(mail.InternalDate),
		Subject:       mail.Subject,
		Message:       body,
		FileName:      filePath,
		InboundNumber: target,
	}

	if customer != nil {
		record.Caller = &pbx3cxv1.VoiceMail_Customer{
			Customer: &customerv1.Customer{
				Id: customer.Id,
			},
		}
	} else {
		record.Caller = &pbx3cxv1.VoiceMail_Number{
			Number: caller,
		}
	}

	if err := box.database.CreateVoiceMail(ctx, record); err != nil {
		slog.ErrorContext(ctx, "failed to create voicemail record", slog.Any("error", err.Error()))
	}

	slog.InfoContext(ctx, "new voicemail received", slog.Any("caller", caller), slog.Any("filePath", filePath), slog.Any("target", target))
}

// Stop stops the mailbox syncer.
func (box *Mailbox) Stop() error {
	return box.syncer.Stop()
}
