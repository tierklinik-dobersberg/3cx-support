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
		GetUpdateInboundNumberCommand(root),
	)

	return cmd
}

func GetCreatePhoneExtensionCommand(root *cli.Root) *cobra.Command {
	var (
		eligibleForOverwrite bool
		internalQueue        bool
	)

	cmd := &cobra.Command{
		Use:     "create [extensions] [display-name] [flags]",
		Aliases: []string{"new", "register"},
		Args:    cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.RegisterPhoneExtensionRequest{
				PhoneExtension: &pbx3cxv1.PhoneExtension{
					Extension:            args[0],
					DisplayName:          args[1],
					EligibleForOverwrite: eligibleForOverwrite,
					InternalQueue:        internalQueue,
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
	cmd.Flags().BoolVarP(&internalQueue, "internal-queue", "i", false, "Wether or not this phone extension is an internal queue")

	return cmd
}

func GetUpdatePhoneExtensionCommand(root *cli.Root) *cobra.Command {
	var (
		eligibleForOverwrite bool
		internalQueue        bool
		extension            string
		displayName          string
	)

	cmd := &cobra.Command{
		Use:  "update [extensions] [flags]",
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			req := &pbx3cxv1.UpdatePhoneExtensionRequest{
				Extension: args[0],
				PhoneExtension: &pbx3cxv1.PhoneExtension{
					Extension:            extension,
					DisplayName:          displayName,
					EligibleForOverwrite: eligibleForOverwrite,
					InternalQueue:        internalQueue,
				},
			}

			var paths []string
			if cmd.Flag("extension").Changed {
				paths = append(paths, "extension")
			}

			if cmd.Flag("display-name").Changed {
				paths = append(paths, "display_name")
			}

			if cmd.Flag("internal-queue").Changed {
				paths = append(paths, "internal_queue")
			}

			if cmd.Flag("eligible-for-overwrite").Changed {
				paths = append(paths, "eligible_for_overwrite")
			}

			res, err := root.CallService().UpdatePhoneExtension(root.Context(), connect.NewRequest(req))
			if err != nil {
				logrus.Fatal(err.Error())
			}

			root.Print(res.Msg)
		},
	}

	f := cmd.Flags()

	f.BoolVarP(&eligibleForOverwrite, "eligible-for-overwrite", "o", false, "Whether or not this extension is eligible for on-call overwrites")
	f.BoolVarP(&internalQueue, "internal-queue", "i", false, "Wether or not this phone extension is an internal queue")
	f.StringVarP(&extension, "extension", "e", "", "The new extension value")
	f.StringVarP(&displayName, "display-name", "d", "", "The new display name")

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
