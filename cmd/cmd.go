// Copyright 2021 ChainSafe Systems
// SPDX-License-Identifier: LGPL-3.0-only

package cmd

import (
	cCLI "github.com/ChainSafe/chainbridge-celo-module/cli"
	"github.com/ChainSafe/chainbridge-core-example/example"
	"github.com/ChainSafe/chainbridge-core-example/server"
	evmCLI "github.com/ChainSafe/chainbridge-core/chains/evm/cli"
	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	rootCMD = &cobra.Command{
		Use: "",
	}
	runCMD = &cobra.Command{
		Use:   "run",
		Short: "Run example app",
		Long:  "Run example app",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := example.Run(); err != nil {
				return err
			}
			return nil
		},
	}

	httpCMD = &cobra.Command{
		Use:   "http",
		Short: "Run server app",
		Long:  "Run server app",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := server.Run(); err != nil {
				return err
			}
			return nil
		},
	}
)

func init() {
	flags.BindFlags(rootCMD)
	rootCMD.PersistentFlags().String(flags.MessageStoreFlagName, "redis://localhost:6379/5", "Specify Redis DB for messagestore")
	_ = viper.BindPFlag(flags.MessageStoreFlagName, rootCMD.PersistentFlags().Lookup(flags.MessageStoreFlagName))

}

func Execute() {
	rootCMD.AddCommand(runCMD, httpCMD, cCLI.CeloRootCLI, evmCLI.EvmRootCLI)
	if err := rootCMD.Execute(); err != nil {
		log.Fatal().Err(err).Msg("failed to execute root cmd")
	}
}
