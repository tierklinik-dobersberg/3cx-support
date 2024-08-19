package cmds

import (
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	commonv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/common/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1/pbx3cxv1connect"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
	"google.golang.org/protobuf/types/known/durationpb"
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
		GetCreateMailboxCommand(root),
		GetSearchVoiceMailRecordsCommand(root),
	)

	return cmd
}

func GetCreateMailboxCommand(root *cli.Root) *cobra.Command {
	var pollInterval time.Duration

	mb := &pbx3cxv1.Mailbox{
		Config: &commonv1.IMAPConfig{},
	}

	cmd := &cobra.Command{
		Use: "create",
		Run: func(cmd *cobra.Command, args []string) {
			cli := pbx3cxv1connect.NewVoiceMailServiceClient(root.HttpClient, root.Config().BaseURLS.CallService)

			mb.PollInterval = durationpb.New(pollInterval)

			res, err := cli.CreateMailbox(root.Context(), connect.NewRequest(&pbx3cxv1.CreateMailboxRequest{
				Mailbox: mb,
			}))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(res.Msg)
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

func GetSearchVoiceMailRecordsCommand(root *cli.Root) *cobra.Command {
	var (
		unseen     bool
		from       string
		to         string
		caller     string
		customerId string
	)

	cmd := &cobra.Command{
		Use:  "records mailbox",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.ListVoiceMailsRequest{
				Mailbox: args[0],
				Filter:  &pbx3cxv1.VoiceMailFilter{},
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

	f := cmd.Flags()
	{
		f.StringVar(&caller, "caller", "", "")
		f.StringVar(&customerId, "customer-id", "", "")
		f.BoolVar(&unseen, "unseen", true, "")
		f.StringVar(&from, "from", "", "")
		f.StringVar(&to, "to", "", "")
	}

	return cmd
}
