package cmds

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	commonv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/common/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1/pbx3cxv1connect"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func GetVoiceMailCommand(root *cli.Root) *cobra.Command {
	cmd := &cobra.Command{
		Use: "voicemail",
		Run: func(cmd *cobra.Command, args []string) {
			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			mailboxes, err := cli.ListMailboxes(root.Context(), connect.NewRequest(&pbx3cxv1.ListMailboxesRequest{}))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(mailboxes.Msg)
		},
	}

	cmd.AddCommand(
		GetCreateOrUpdateMailboxCommand(root),
		GetAddNotificationSettingsCommand(root),
		GetDeleteNotificationSettingCommand(root),
		GetSearchVoiceMailRecordsCommand(root),
	)

	return cmd
}

func GetCreateOrUpdateMailboxCommand(root *cli.Root) *cobra.Command {
	var pollInterval time.Duration

	mb := &pbx3cxv1.Mailbox{
		Config: &commonv1.IMAPConfig{},
	}

	cmd := &cobra.Command{
		Use:     "create",
		Aliases: []string{"update"},
		Run: func(cmd *cobra.Command, args []string) {
			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			mb.PollInterval = durationpb.New(pollInterval)

			if cmd.CalledAs() == "create" {
				res, err := cli.CreateMailbox(root.Context(), connect.NewRequest(&pbx3cxv1.CreateMailboxRequest{
					Mailbox: mb,
				}))
				if err != nil {
					logrus.Fatal(err.Error())
				}

				root.Print(res.Msg)
			} else {
				if len(args) == 0 {
					logrus.Fatalf("missing mailbox id parameter")
				}

				res, err := cli.UpdateMailbox(root.Context(), connect.NewRequest(&pbx3cxv1.UpdateMailboxRequest{
					MailboxId: args[0],
					Update: &pbx3cxv1.UpdateMailboxRequest_Mailbox{
						Mailbox: mb,
					},
				}))

				if err != nil {
					logrus.Fatal(err.Error())
				}

				root.Print(res.Msg)
			}
		},
	}

	f := cmd.Flags()
	{
		f.StringVar(&mb.DisplayName, "display-name", "", "")
		f.StringVar(&mb.ExtractCallerRegexp, "caller-regexp", "", "")
		f.StringVar(&mb.ExtractTargetRegexp, "target-regexp", "", "")
		f.StringVar(&mb.Config.Host, "host", "", "")
		f.StringVar(&mb.Config.Folder, "folder", "", "")
		f.StringVar(&mb.Config.User, "user", "", "")
		f.StringVar(&mb.Config.Password, "password", "", "")
		f.BoolVar(&mb.Config.Tls, "tls", true, "")
		f.BoolVar(&mb.Config.InsecureSkipVerify, "insecure", false, "")
		f.BoolVar(&mb.Config.ReadOnly, "read-only", true, "")
		f.DurationVar(&pollInterval, "poll-interval", time.Minute*5, "")
	}

	return cmd
}

func GetDeleteNotificationSettingCommand(root *cli.Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "delete-notification [id] [setting-name]",
		Args: cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			res, err := cli.UpdateMailbox(root.Context(), connect.NewRequest(&pbx3cxv1.UpdateMailboxRequest{
				MailboxId: args[0],
				Update: &pbx3cxv1.UpdateMailboxRequest_DeleteNotificationSetting{
					DeleteNotificationSetting: args[1],
				},
			}))
			if err != nil {
				logrus.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	return cmd
}

func GetAddNotificationSettingsCommand(root *cli.Root) *cobra.Command {
	var (
		name            string
		description     string
		subjectTemplate string
		messageTemplate string
		users           []string
		times           []string
		types           []string
	)

	cmd := &cobra.Command{
		Use:  "add-notification [id]",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.NotificationSettings{
				Name:            name,
				Description:     description,
				SubjectTemplate: subjectTemplate,
				MessageTemplate: messageTemplate,
			}

			usersIds, err := root.ResolveUserIds(root.Context(), users)
			if err != nil {
				logrus.Fatalf("failed to resolve users: %s", err)
			}

			req.Recipients = &pbx3cxv1.NotificationSettings_UserIds{
				UserIds: &commonv1.StringList{
					Values: usersIds,
				},
			}

			// parse and append send times
			for _, t := range times {
				parts := strings.Split(t, ":")
				if len(parts) != 2 {
					logrus.Fatalf("failed to parse time-of-day %q: %s", t, err)
				}

				hour, err := strconv.ParseInt(strings.TrimPrefix(parts[0], "0"), 10, 0)
				if err != nil {
					logrus.Fatalf("failed to parse time-of-day: invalid hour %q: %s", parts[0], err)
				}

				minute, err := strconv.ParseInt(strings.TrimPrefix(parts[1], "0"), 10, 0)
				if err != nil {
					logrus.Fatalf("failed to parse time-of-day: invalid minute %q: %s", parts[1], err)
				}

				req.SendTimes = append(req.SendTimes, &commonv1.DayTime{
					Hour:   int32(hour),
					Minute: int32(minute),
				})
			}

			// parse and append notification-types
			for _, t := range types {
				switch t {
				case "sms":
					req.Types = append(req.Types, pbx3cxv1.NotificationType_NOTIFICATION_TYPE_SMS)
				case "mail", "email":
					req.Types = append(req.Types, pbx3cxv1.NotificationType_NOTIFICATION_TYPE_MAIL)
				case "push", "webpush":
					req.Types = append(req.Types, pbx3cxv1.NotificationType_NOTIFICATION_TYPE_WEBPUSH)

				default:
					logrus.Fatalf("unsupported or invalid notification type %q", t)
				}
			}

			root.Print(req)
			fmt.Println()

			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			res, err := cli.UpdateMailbox(root.Context(), connect.NewRequest(&pbx3cxv1.UpdateMailboxRequest{
				MailboxId: args[0],
				Update: &pbx3cxv1.UpdateMailboxRequest_AddNotificationSetting{
					AddNotificationSetting: req,
				},
			}))
			if err != nil {
				logrus.Fatalf(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	f := cmd.Flags()
	{
		f.StringVar(&name, "name", "", "The name for the notification setting")
		cmd.MarkFlagRequired("name")

		f.StringVar(&description, "description", "", "An optional description for the notification settings")

		f.StringVar(&subjectTemplate, "subject", "", "The template for the notification subject (only for WebPush and EMail)")
		f.StringVar(&messageTemplate, "message", "", "The template for the notification message")
		f.StringSliceVar(&users, "to-user", nil, "A list of users to notify")
		f.StringSliceVar(&times, "send-at", nil, "A list of time-of-day (HH:MM) at which notification should be sent")

		f.StringSliceVar(&types, "type", nil, "A list of notification types to send. Valid values are sms, push or mail")
		cmd.MarkFlagRequired("type")
	}

	return cmd
}

func GetSearchVoiceMailRecordsCommand(root *cli.Root) *cobra.Command {
	var (
		unseen     bool
		from       string
		to         string
		caller     string
		customerId string
		paths      []string
		prune      bool
	)

	cmd := &cobra.Command{
		Use:  "records mailbox",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.ListVoiceMailsRequest{
				Mailbox: args[0],
				Filter:  &pbx3cxv1.VoiceMailFilter{},
				View: &commonv1.View{
					FieldMask: &fieldmaskpb.FieldMask{
						Paths: paths,
					},
					Prune: prune,
				},
			}

			if cmd.Flag("unseen").Changed {
				req.Filter.Unseen = wrapperspb.Bool(unseen)
			}

			if cmd.Flag("caller").Changed {
				req.Filter.Caller = &pbx3cxv1.VoiceMailFilter_Number{
					Number: caller,
				}
			} else if cmd.Flag("customer-id").Changed {
				req.Filter.Caller = &pbx3cxv1.VoiceMailFilter_CustomerId{
					CustomerId: customerId,
				}
			}

			tr := &commonv1.TimeRange{}
			if from != "" {
				fromTime, err := time.Parse(time.DateTime, from)
				if err != nil {
					logrus.Fatalf("invalid --from time, expected layout is %q", time.DateTime)
				}

				tr.From = timestamppb.New(fromTime)
			}

			if to != "" {
				toTime, err := time.Parse(time.DateTime, to)
				if err != nil {
					logrus.Fatalf("invalid --to time, expected layout is %q", time.DateTime)
				}

				tr.To = timestamppb.New(toTime)
			}

			if from != "" || to != "" {
				req.Filter.TimeRange = tr
			}

			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			res, err := cli.ListVoiceMails(root.Context(), connect.NewRequest(req))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	cmd.AddCommand(
		GetFetchVoiceMailCommand(root),
		GetVoiceMailRecordCommand(root),
		GetMarkVoiceMailCommand(root),
	)

	f := cmd.Flags()
	{
		f.StringVar(&caller, "caller", "", "")
		f.StringVar(&customerId, "customer-id", "", "")
		f.BoolVar(&unseen, "unseen", true, "")
		f.BoolVar(&prune, "exclude-fields", false, "Whether or not --field should be included or excluded")
		f.StringVar(&from, "from", "", "")
		f.StringVar(&to, "to", "", "")
		f.StringSliceVar(&paths, "field", nil, "")
	}

	return cmd
}

func GetVoiceMailRecordCommand(root *cli.Root) *cobra.Command {
	var (
		paths []string
		prune bool
	)

	cmd := &cobra.Command{
		Use:  "get id",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.GetVoiceMailRequest{
				Id: args[0],
				View: &commonv1.View{
					FieldMask: &fieldmaskpb.FieldMask{
						Paths: paths,
					},
					Prune: prune,
				},
			}

			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			res, err := cli.GetVoiceMail(root.Context(), connect.NewRequest(req))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	f := cmd.Flags()
	{
		f.BoolVar(&prune, "exclude-fields", false, "Whether or not --field should be included or excluded")
		f.StringSliceVar(&paths, "field", nil, "A list of protobuf field names to include or exclude")
	}

	return cmd
}

func GetFetchVoiceMailCommand(root *cli.Root) *cobra.Command {
	var (
		outputFile string
	)

	cmd := &cobra.Command{
		Use:  "fetch [id]",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			// FIXME(ppacher): fetch recording and use the existing filename
			// instead of foricng --output to be set.
			if outputFile == "" {
				logrus.Fatalf("--output is required")
			}

			u, err := url.Parse(root.Config().BaseURLS.CallService)
			if err != nil {
				logrus.Fatalf("invalid URI: %s", err)
			}

			u.Path = "/voicemails/"

			q := u.Query()

			q.Add("id", args[0])

			u.RawQuery = q.Encode()

			req, err := http.NewRequestWithContext(root.Context(), http.MethodGet, u.String(), nil)
			if err != nil {
				logrus.Fatalf("failed to prepare request: %s", err)
			}

			req.Header.Add("Authorization", "Bearer "+root.Tokens().AccessToken)

			res, err := root.HttpClient.Do(req)
			if err != nil {
				logrus.Fatalf("error fetching recording: %s", err.Error())
			}
			defer res.Body.Close()

			if res.StatusCode != http.StatusOK {
				logrus.Fatalf("error fetching recording: %s", res.Status)
			}

			var output io.Writer
			switch outputFile {
			case "-":
				output = os.Stdout
			default:
				f, err := os.Create(outputFile)
				if err != nil {
					logrus.Fatalf("failed to create output file: %s", err.Error())
				}
				defer f.Close()

				output = f
			}

			if _, err := io.Copy(output, res.Body); err != nil {
				logrus.Fatalf("failed to write output file: %s", err.Error())
			}
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Desitnation for the recording file")

	return cmd
}

func GetMarkVoiceMailCommand(root *cli.Root) *cobra.Command {
	var seen bool

	cmd := &cobra.Command{
		Use:  "mark [ids...]",
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			res, err := cli.MarkVoiceMails(root.Context(), connect.NewRequest(&pbx3cxv1.MarkVoiceMailsRequest{
				VoicemailIds: args,
				Seen:         seen,
			}))

			if err != nil {
				logrus.Fatalf(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().BoolVar(&seen, "seen", true, "Mark as seen or unseen")

	return cmd
}
