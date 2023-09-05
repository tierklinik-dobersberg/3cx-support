package main

import (
	"github.com/sirupsen/logrus"
	"github.com/tierklinik-dobersberg/3cx-support/cmd/callctl/cmds"
	"github.com/tierklinik-dobersberg/apis/pkg/cli"
)

func main() {
	root := cli.New("callctl")

	root.AddCommand(
		cmds.GetCallLogCommand(root),
		cmds.GetOnDutyCommand(root),
	)

	if err := root.Execute(); err != nil {
		logrus.Fatal(err)
	}
}
