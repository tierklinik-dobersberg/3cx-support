package cmds

import (
	"context"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	commonv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/common/v1"
	customerv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/customer/v1"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func GetCallLogCommand(root *cli.Root) *cobra.Command {
	var (
		fromStr        string
		toStr          string
		date           string
		customerSource string
		customerId     string
	)
	cmd := &cobra.Command{
		Use:     "call-logs",
		Aliases: []string{"logs"},
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.SearchCallLogsRequest{
				Date: date,
			}

			if customerId != "" || customerSource != "" {
				if customerId == "" || customerSource == "" {
					logrus.Fatal("--customer-id and --customer-source must both be set")
				}

				req.CustomerRef = &customerv1.CustomerRef{
					Source: customerSource,
					Id:     customerId,
				}
			}

			if fromStr != "" || toStr != "" {
				if fromStr == "" || toStr == "" {
					logrus.Fatal("--from and --to must both be set")
				}

				if date != "" {
					logrus.Fatal("either --date or (--to and --from) may be used")
				}

				from, err := time.Parse(time.RFC3339, fromStr)
				if err != nil {
					logrus.Fatal("invalid value for --from")
				}

				to, err := time.Parse(time.RFC3339, toStr)
				if err != nil {
					logrus.Fatal("invalid value for --to")
				}

				req.TimeRange = &commonv1.TimeRange{
					From: timestamppb.New(from),
					To:   timestamppb.New(to),
				}
			}

			res, err := root.CallService().SearchCallLogs(context.Background(), connect.NewRequest(req))
			if err != nil {
				logrus.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	f := cmd.Flags()
	{
		f.StringVar(&fromStr, "from", "", "")
		f.StringVar(&toStr, "to", "", "")
		f.StringVar(&date, "date", "", "")
		f.StringVar(&customerId, "customer-id", "", "")
		f.StringVar(&customerSource, "customer-source", "", "")
	}

	return cmd
}
