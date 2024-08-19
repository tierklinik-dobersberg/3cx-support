package voicemail

import (
	"context"
	"fmt"
	"sync"

	"github.com/hashicorp/go-multierror"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/3cx-support/internal/database"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/mailsync"
)

type Manager struct {
	*mailsync.Manager

	providers *config.Providers

	l     sync.Mutex
	boxes map[string]*Mailbox
}

func NewManager(ctx context.Context, providers *config.Providers) (*Manager, error) {
	mailsyncManager, err := mailsync.NewManager(ctx, providers.MailboxDatabase)
	if err != nil {
		return nil, err
	}

	mng := &Manager{
		providers: providers,
		Manager:   mailsyncManager,
		boxes:     make(map[string]*Mailbox),
	}

	return mng, mng.start(ctx)
}

func (mng *Manager) start(ctx context.Context) error {
	mailboxes, err := mng.providers.MailboxDatabase.ListMailboxes(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch existing mailboxes: %w", err)
	}

	mng.l.Lock()
	defer mng.l.Unlock()

	var merr = new(multierror.Error)
	for _, mb := range mailboxes {
		box, err := NewMailboxSyncer(ctx, mng.providers.Config.VoiceMailStoragePath, mng.Manager, mng.providers.MailboxDatabase, mng.providers.Customer, mb)
		if err != nil {
			merr.Errors = append(merr.Errors, fmt.Errorf("failed to create mailbox %q: %w", mb.Id, err))
			continue
		}

		mng.boxes[mb.Id] = box
	}

	return merr.ErrorOrNil()
}

func (mng *Manager) CreateMailbox(ctx context.Context, mb *pbx3cxv1.Mailbox) error {
	if err := mng.providers.MailboxDatabase.CreateMailbox(ctx, mb); err != nil {
		return err
	}

	box, err := NewMailboxSyncer(
		ctx,
		mng.providers.Config.VoiceMailStoragePath,
		mng.Manager,
		mng.providers.MailboxDatabase,
		mng.providers.Customer,
		mb,
	)
	if err != nil {
		return fmt.Errorf("failed to create mailbox syncer %q: %w", mb.Id, err)
	}

	mng.l.Lock()
	defer mng.l.Unlock()
	mng.boxes[mb.Id] = box

	return nil
}

func (mng *Manager) DeleteMailbox(ctx context.Context, id string) error {
	mng.l.Lock()
	defer mng.l.Unlock()

	box, ok := mng.boxes[id]
	if !ok {
		return database.ErrNotFound
	}

	if err := box.Dispose(); err != nil {
		return fmt.Errorf("failed to dispose syncer %q: %w", id, err)
	}

	delete(mng.boxes, id)

	// TODO(ppacher): delete from database

	return nil
}
