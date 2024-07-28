package cmds

import (
	"log"

	"github.com/bufbuild/connect-go"
	"github.com/spf13/cobra"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
)

func GetInboundNumbersCommand(root *cli.Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "inbound-numbers",
		Aliases: []string{"inbound"},
		Run: func(cmd *cobra.Command, args []string) {
			res, err := root.CallService().ListInboundNumber(root.Context(), connect.NewRequest(&pbx3cxv1.ListInboundNumberRequest{}))
			if err != nil {
				log.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	cmd.AddCommand(
		GetCreateInboundNumberCommand(root),
		GetUpdateInboundNumberCommand(root),
	)

	return cmd
}

func GetCreateInboundNumberCommand(root *cli.Root) *cobra.Command {
	var displayName string

	cmd := &cobra.Command{
		Use:     "create [number] [flags]",
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"add"},
		Run: func(cmd *cobra.Command, args []string) {
			svc := root.CallService()

			res, err := svc.CreateInboundNumber(root.Context(), connect.NewRequest(&pbx3cxv1.CreateInboundNumberRequest{
				Number:      args[0],
				DisplayName: displayName,
			}))

			if err != nil {
				log.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().StringVarP(&displayName, "display-name", "d", "", "An optional display name for the inbound number")

	return cmd
}

func GetUpdateInboundNumberCommand(root *cli.Root) *cobra.Command {
	var displayName string

	cmd := &cobra.Command{
		Use:     "update [number] [flags]",
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"set"},
		Run: func(cmd *cobra.Command, args []string) {
			svc := root.CallService()

			res, err := svc.UpdateInboundNumber(root.Context(), connect.NewRequest(&pbx3cxv1.UpdateInboundNumberRequest{
				Number:         args[0],
				NewDisplayName: displayName,
			}))

			if err != nil {
				log.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().StringVarP(&displayName, "display-name", "d", "", "An optional display name for the inbound number")

	return cmd
}

func GetDeleteInboundNumberCommand(root *cli.Root) *cobra.Command {
	var displayName string

	cmd := &cobra.Command{
		Use:     "delete [number]",
		Args:    cobra.ExactArgs(1),
		Aliases: []string{"rm"},
		Run: func(cmd *cobra.Command, args []string) {
			svc := root.CallService()

			res, err := svc.DeleteInboundNumber(root.Context(), connect.NewRequest(&pbx3cxv1.DeleteInboundNumberRequest{
				Number: args[0],
			}))

			if err != nil {
				log.Fatal(err)
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().StringVarP(&displayName, "display-name", "d", "", "An optional display name for the inbound number")

	return cmd
}
