package cmds

import (
	"github.com/bufbuild/connect-go"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	pbx3cxv1 "github.com/tierklinik-dobersberg/apis/gen/go/tkd/pbx3cx/v1"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
)

func GetPhoneExtensionsCommand(root *cli.Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "phone-extensions",
		Aliases: []string{"phone-ext", "ext"},
		Run: func(cmd *cobra.Command, args []string) {
			res, err := root.CallService().ListPhoneExtensions(root.Context(), connect.NewRequest(&pbx3cxv1.ListPhoneExtensionsRequest{}))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	cmd.AddCommand(
		GetCreatePhoneExtensionCommand(root),
		GetDeletePhoneExtensionCommand(root),
	)

	return cmd
}

func GetCreatePhoneExtensionCommand(root *cli.Root) *cobra.Command {
	var (
		eligibleForOverwrite bool
	)

	cmd := &cobra.Command{
		Use:     "create [extensions] [display-name] [flags]",
		Aliases: []string{"new", "register", "update"},
		Args:    cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.RegisterPhoneExtensionRequest{
				PhoneExtension: &pbx3cxv1.PhoneExtension{
					Extension:            args[0],
					DisplayName:          args[1],
					EligibleForOverwrite: eligibleForOverwrite,
				},
			}

			res, err := root.CallService().RegisterPhoneExtension(root.Context(), connect.NewRequest(req))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	cmd.Flags().BoolVarP(&eligibleForOverwrite, "eligible-for-overwrite", "o", false, "Whether or not this extension is eligible for on-call overwrites")

	return cmd
}

func GetDeletePhoneExtensionCommand(root *cli.Root) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "delete [extension]",
		Aliases: []string{"deregister", "remove"},
		Args:    cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.DeletePhoneExtensionRequest{
				Extension: args[0],
			}

			_, err := root.CallService().DeletePhoneExtension(root.Context(), connect.NewRequest(req))
			if err != nil {
				logrus.Fatal(err.Error())
			}
		},
	}
	return cmd
}
