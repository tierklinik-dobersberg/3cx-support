package cmds

import (
	"context"
	"time"

	"github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
)

func GetOnDutyCommand(root *cli.Root) *cobra.Command {
	var (
		ignoreOverwrites bool
		date             string
		inboundNumber    string
	)

	cmd := &cobra.Command{
		Use:     "on-duty",
		Aliases: []string{"on-call"},
		Run: func(cmd *cobra.Command, args []string) {
			res, err := root.CallService().GetOnCall(context.Background(), connect.NewRequest(&pbx3cxv1.GetOnCallRequest{
				IgnoreOverwrites: ignoreOverwrites,
				Date:             date,
				InboundNumber:    inboundNumber,
			}))

			if err != nil {
				logrus.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().StringVar(&date, "date", "", "The date for which on-call should be returned. Format: "+time.RFC3339)
	cmd.Flags().StringVar(&inboundNumber, "number", "", "The inbound number for which on-call should be returned")
	cmd.Flags().BoolVar(&ignoreOverwrites, "ingore-overwrites", false, "Whether or not overwrites should be ignored.")

	return cmd
}
