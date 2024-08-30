package services

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	eventsv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/events/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	rosterv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/roster/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
	"github.com/tierklinik-dobersberg/apis/pkg/events"
	"google.golang.org/protobuf/proto"
)

type OnCallCache struct {
	inboundNumber string
	providers     *config.Providers
	trigger       chan struct{}
	events        *events.Client

	l      sync.RWMutex
	onCall *pbx3cxv1.GetOnCallResponse
}

func NewOnCallCache(ctx context.Context, inboundNumber string, providers *config.Providers) (*OnCallCache, error) {
	// setup the event listener
	eventClient := events.NewClient(providers.Config.EventsServiceURL, cli.NewInsecureHttp2Client())

	cache := &OnCallCache{
		providers:     providers,
		inboundNumber: inboundNumber,
		trigger:       make(chan struct{}),
		events:        eventClient,
	}

	if err := cache.events.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start events client: %w", err)
	}

	// subscribe to roster-change events
	ch, err := cache.events.SubscribeMessage(ctx, &rosterv1.RosterChangedEvent{})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to %q", proto.MessageName(&rosterv1.RosterChangedEvent{}))
	}

	slog.Info("succesfully subscribed to roster change events", "typeUrl", proto.MessageName(&rosterv1.RosterChangedEvent{}))

	go cache.run(ctx, ch)

	return cache, nil
}

func (cache *OnCallCache) Trigger() {
	cache.trigger <- struct{}{}
}

func (cache *OnCallCache) run(ctx context.Context, events <-chan *eventsv1.Event) {
	// update every 5 minutes by default
	ticker := time.NewTicker(time.Minute * 5)
	defer ticker.Stop()

	for {
		onCall, err := cache.providers.ResolveOnCallTarget(ctx, time.Now(), false, cache.inboundNumber)
		if err != nil {
			slog.Error("cache: failed to resolve on-call target", "error", err, "inbound-number", cache.inboundNumber)
			continue
		}

		changed := false
		cache.l.Lock()

		if cache.onCall == nil {
			changed = true
		} else {
			changed = onCall.PrimaryTransferTarget != cache.onCall.PrimaryTransferTarget
		}

		cache.onCall = onCall
		cache.l.Unlock()

		if changed {
			var t time.Time

			for _, onCall := range onCall.OnCall {
				if t.IsZero() || onCall.Until.AsTime().Before(t) {
					t = onCall.Until.AsTime()
				}
			}

			if !t.IsZero() {
				go func() {
					slog.Info("waiting for on-call to change", "expectedChangeTime", t.Format(time.RFC3339))
					<-time.After(time.Until(t))

					slog.Info("triggering update since on-call is about to change")
					cache.Trigger()
				}()
			}

			slog.Info("cache update complete, new on-call target found", "inboundNumber", cache.inboundNumber, "on-call", onCall.PrimaryTransferTarget)

			evt := &pbx3cxv1.OnCallChangeEvent{
				OnCall:                onCall.OnCall,
				RosterDate:            onCall.RosterDate,
				IsOverwrite:           onCall.IsOverwrite,
				PrimaryTransferTarget: onCall.PrimaryTransferTarget,
				InboundNumber:         cache.inboundNumber,
			}

			cache.providers.PublishEvent(evt, true)
		} else {
			slog.Info("cache update complete, on-call target unchanged", "inboundNumber", cache.inboundNumber, "on-call", onCall.PrimaryTransferTarget)
		}

		select {
		case <-ticker.C:
			slog.Info("cache timeout, triggering update", "inboundNumber", cache.inboundNumber)
		case <-cache.trigger:
			slog.Info("manual cache update triggered", "inboundNumber", cache.inboundNumber)
		case <-events:
			slog.Info("roster event received, triggering update", "inboundNumber", cache.inboundNumber)
		case <-ctx.Done():
			return
		}
	}
}

func (cache *OnCallCache) Current() *pbx3cxv1.GetOnCallResponse {
	cache.l.RLock()
	defer cache.l.RUnlock()

	if cache.onCall == nil {
		// TODO trigger sync
		return nil
	}

	return proto.Clone(cache.onCall).(*pbx3cxv1.GetOnCallResponse)
}
