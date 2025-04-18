package worker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"text/template"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/tierklinik-dobersberg/3cx-support/internal/config"
	"github.com/tierklinik-dobersberg/3cx-support/internal/voicemail"
	idmv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/idm/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
)

func StartNotificationWorker(ctx context.Context, mng *voicemail.Manager, providers *config.Providers) {
	startTime := time.Now()

	ticker := time.NewTicker(time.Minute)
	lastSentMap := make(map[string]time.Time)

	l := slog.Default().With("subsystem", "notification-worker")

	go func() {
		for {
			select {
			case <-ctx.Done():
				slog.Info("notification worker cancelled")

				return
			case <-ticker.C:
			}

			// fetch all voicemail boxes
			mailboxes, err := providers.MailboxDatabase.ListMailboxes(ctx)
			if err != nil {
				l.ErrorContext(ctx, "failed to retrieve mailbox list", slog.Any("error", err.Error()))
			}

			l.Info("loaded mailboxes for unseen-voicemail notifications", "count", len(mailboxes))

			for _, mb := range mailboxes {
				lm := l.With("mailbox", mb.Id)

				lm.Info("triggering mailbox sync ...")
				// trigger and wait for the mailbox to sync so we don't miss any mails
				triggerCtx, cancel := context.WithTimeout(ctx, time.Second*30)
				if err := mng.TriggerSync(triggerCtx, mb.Id); err != nil {
					if ctx.Err() != nil {
						cancel()
						return
					}

					lm.ErrorContext(ctx, "failed to trigger mailbox sync", slog.Any("error", err.Error()))

					// still, continue and check if we need to send notifications.
				}
				cancel()

				// iterate over all notification settings
				for idx, nfs := range mb.NotificationSettings {
					lnfs := lm.With("notification-setting", nfs.Name)

					// find notification candidates
					candidates, err := providers.MailboxDatabase.FindNotificationCandidates(ctx, mb.Id, true, nfs.Name)
					if err != nil {
						lnfs.Error("failed to find notification candiates", "error", err)
						continue
					}

					if len(candidates) == 0 {
						lnfs.Info("no notification candidates found")
						continue
					}

					lnfs.Info("found candiates for notifications", "count", len(candidates))

					reqs, err := newNotificationRequests(providers.Config.NotificationSenderId, mb, nfs, len(candidates), lnfs)
					if err != nil {
						lnfs.ErrorContext(ctx, "failed to create notification requests", slog.Any("error", err.Error()))
						continue
					}

					now := time.Now().Local()

					for _, t := range nfs.SendTimes {
						sendTimeToday := time.Date(now.Year(), now.Month(), now.Day(), int(t.Hour), int(t.Minute), int(t.Second), 0, time.Local)

						// Do not send notifications for time-of-day entries that
						// occured before the worker even started
						if sendTimeToday.Before(startTime) || sendTimeToday.After(now) {
							lnfs.Info("skipping notification as sendTime is before start time or after now", "send-time", sendTimeToday.Format(time.RFC3339), "start-time", startTime.Format(time.RFC3339))

							continue
						}

						key := mb.Id + fmt.Sprintf("-%d-%d:%d:%d", idx, t.Hour, t.Minute, t.Second)
						lastSent, ok := lastSentMap[key]

						success := true

						if !ok || lastSent.Before(sendTimeToday) {
							lnfs.InfoContext(ctx, "sending notification requests for time-of-day", slog.Any("key", key))

							for _, r := range reqs {
								res, err := providers.Notify.SendNotification(ctx, connect.NewRequest(r))
								if err != nil {
									lnfs.ErrorContext(ctx, "failed to send notification", slog.Any("key", key), slog.Any("error", err.Error()))
									success = false
								} else {
									for _, d := range res.Msg.Deliveries {
										if d.ErrorKind != idmv1.ErrorKind_ERROR_KIND_UNSPECIFIED {
											lnfs.ErrorContext(ctx, "failed to send notification", slog.Any("key", key), slog.Any("error", d.Error), slog.Any("errorKind", d.ErrorKind.String()))
											success = false
										}
									}
								}
							}

							if success {
								if err := providers.MailboxDatabase.MarkAsNotificationSent(ctx, mb.Id, nfs.Name, candidates); err != nil {
									lnfs.Error("failed to mark voicemail notifications as sent", "error", err)
								}
							} else {
								lnfs.Error("not marking voicemail notification as sent, an error occured")
							}

							lastSentMap[key] = sendTimeToday
						} else {
							lnfs.Info("not sending notification", "last", lastSent, "next", sendTimeToday, "key", key)
						}
					}
				}
			}
		}
	}()
}

func newNotificationRequests(sender string, mb *pbx3cxv1.Mailbox, nfs *pbx3cxv1.NotificationSettings, count int, log *slog.Logger) ([]*idmv1.SendNotificationRequest, error) {
	// create and parse the message and subject templates
	msgTmpl, err := template.New("").Parse(nfs.MessageTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse message template: %w", err)
	}

	subjTmpl, err := template.New("").Parse(nfs.SubjectTemplate)
	if err != nil {
		return nil, fmt.Errorf("failed to parse subject template: %w", err)
	}

	// generate the message
	msg := new(strings.Builder)
	sbj := new(strings.Builder)

	// construct the context
	tCtx := map[string]any{
		"count": count,
		"name":  mb.DisplayName,
	}

	// execute message and subject templates
	if err := msgTmpl.Execute(msg, tCtx); err != nil {
		return nil, fmt.Errorf("failed to execute message template: %w", err)
	}

	if err := subjTmpl.Execute(sbj, tCtx); err != nil {
		return nil, fmt.Errorf("failed to execute subject template: %w", err)
	}

	var results []*idmv1.SendNotificationRequest

	for _, nType := range nfs.Types {
		req := &idmv1.SendNotificationRequest{
			SenderUserId: sender,
		}

		switch v := nfs.Recipients.(type) {
		case *pbx3cxv1.NotificationSettings_RoleIds:
			req.TargetRoles = v.RoleIds.GetValues()
		case *pbx3cxv1.NotificationSettings_UserIds:
			req.TargetUsers = v.UserIds.GetValues()

		default:
			log.Warn("unspecified or unsupported reciepient: %T", v)
			continue
		}

		switch nType {
		case pbx3cxv1.NotificationType_NOTIFICATION_TYPE_MAIL:
			req.Message = &idmv1.SendNotificationRequest_Email{
				Email: &idmv1.EMailMessage{
					Subject: sbj.String(),
					Body:    msg.String(),
				},
			}

		case pbx3cxv1.NotificationType_NOTIFICATION_TYPE_SMS:
			req.Message = &idmv1.SendNotificationRequest_Sms{
				Sms: &idmv1.SMS{
					Body: msg.String(),
				},
			}

		case pbx3cxv1.NotificationType_NOTIFICATION_TYPE_WEBPUSH:
			req.Message = &idmv1.SendNotificationRequest_Webpush{
				Webpush: &idmv1.WebPushNotification{
					Kind: &idmv1.WebPushNotification_Notification{
						Notification: &idmv1.ServiceWorkerNotification{
							Title: sbj.String(),
							Body:  msg.String(),
						},
					},
				},
			}

		case pbx3cxv1.NotificationType_NOTIFICATION_TYPE_UNSPECIFIED:
			fallthrough

		default:
			log.Warn("unspecified or unsupported notification type: %s", nType)
		}

		results = append(results, req)
	}

	return results, nil
}
