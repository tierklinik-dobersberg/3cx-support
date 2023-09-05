package cmds

import (
	"context"

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
	)

	cmd := &cobra.Command{
		Use:     "on-duty",
		Aliases: []string{"on-call"},
		Run: func(cmd *cobra.Command, args []string) {
			res, err := root.CallService().GetOnCall(context.Background(), connect.NewRequest(&pbx3cxv1.GetOnCallRequest{
				IgnoreOverwrites: ignoreOverwrites,
				Date:             date,
			}))

			if err != nil {
				logrus.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().StringVar(&date, "date", "", "")
	cmd.Flags().BoolVar(&ignoreOverwrites, "ingore-overwrites", false, "")

	return cmd
}
