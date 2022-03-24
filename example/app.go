// Copyright 2021 ChainSafe Systems
// SPDX-License-Identifier: LGPL-3.0-only

package example

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChainSafe/chainbridge-celo-module/transaction"
	"github.com/ChainSafe/chainbridge-core/chains/evm"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/bridge"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc1155"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc20"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc721"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmclient"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmgaspricer"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmtransaction"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/transactor/signAndSend"
	"github.com/ChainSafe/chainbridge-core/chains/evm/listener"
	"github.com/ChainSafe/chainbridge-core/chains/evm/voter"
	"github.com/ChainSafe/chainbridge-core/config"
	"github.com/ChainSafe/chainbridge-core/config/chain"
	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/ChainSafe/chainbridge-core/lvldb"
	"github.com/ChainSafe/chainbridge-core/relayer"
	"github.com/ChainSafe/chainbridge-core/relayer/message"
	"github.com/ChainSafe/chainbridge-core/store"
	optimism "github.com/ChainSafe/chainbridge-optimism-module"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-redis/redis/v8"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

type EvmContracts struct {
	bridge         *bridge.BridgeContract
	erc1155Handler *erc1155.ERC1155HandlerContract
	erc20Handler   *erc20.ERC20HandlerContract
	erc721Handler  *erc721.ERC721HandlerContract
}

var (
	chains map[uint8]EvmContracts
)

func init() {
	chains = make(map[uint8]EvmContracts)
}

func Run() error {
	errChn := make(chan error)
	stopChn := make(chan struct{})

	configuration, err := config.GetConfig(viper.GetString(flags.ConfigFlagName))
	db, err := lvldb.NewLvlDB(viper.GetString(flags.BlockstoreFlagName))
	if err != nil {
		panic(err)
	}
	blockstore := store.NewBlockStore(db)

	opts, err := redis.ParseURL(viper.GetString(flags.MessageStoreFlagName))
	if err != nil {
		return err
	}
	redisDB := redis.NewClient(opts)
	messagestore := DepositMessageStore{redisDB}
	telemetry := &LevelDBTelemetry{&messagestore}

	chains := []relayer.RelayedChain{}
	for _, chainConfig := range configuration.ChainConfigs {
		switch chainConfig["type"] {
		case "evm":
			{
				chain, err := evm.SetupDefaultEVMChain(chainConfig, evmtransaction.NewTransaction, blockstore)
				if err != nil {
					panic(err)
				}

				chains = append(chains, chain)
			}
		case "celo":
			{
				config, err := chain.NewEVMConfig(chainConfig)
				if err != nil {
					panic(err)
				}
				client, err := evmclient.NewEVMClient(config)
				if err != nil {
					panic(err)
				}
				gasPricer := evmgaspricer.NewStaticGasPriceDeterminant(client, nil)
				t := signAndSend.NewSignAndSendTransactor(transaction.NewCeloTransaction, gasPricer, client)
				bridgeContract := bridge.NewBridgeContract(client, common.HexToAddress(config.Bridge), t)

				eventHandler := listener.NewETHEventHandler(*bridgeContract)
				eventHandler.RegisterEventHandler(config.Erc20Handler, listener.Erc20EventHandler)
				eventHandler.RegisterEventHandler(config.Erc721Handler, listener.Erc721EventHandler)
				eventHandler.RegisterEventHandler(config.GenericHandler, listener.GenericEventHandler)
				evmListener := listener.NewEVMListener(client, eventHandler, common.HexToAddress(config.Bridge))

				mh := voter.NewEVMMessageHandler(*bridgeContract)
				mh.RegisterMessageHandler(config.Erc20Handler, voter.ERC20MessageHandler)
				mh.RegisterMessageHandler(config.Erc721Handler, voter.ERC721MessageHandler)
				mh.RegisterMessageHandler(config.GenericHandler, voter.GenericMessageHandler)

				evmVoter := voter.NewVoter(mh, client, bridgeContract)
				chains = append(chains, evm.NewEVMChain(evmListener, evmVoter, blockstore, config))
			}
		case "optimism":
			{
				chain, err := optimism.SetupDefaultOptimismChain(chainConfig, evmtransaction.NewTransaction, blockstore)
				if err != nil {
					panic(err)
				}

				chains = append(chains, chain)
			}
		}
	}

	r := relayer.NewRelayer(chains, telemetry)
	go r.Start(stopChn, errChn)

	sysErr := make(chan os.Signal, 1)
	signal.Notify(sysErr,
		syscall.SIGTERM,
		syscall.SIGINT,
		syscall.SIGHUP,
		syscall.SIGQUIT)

	select {
	case err := <-errChn:
		log.Error().Err(err).Msg("failed to listen and serve")
		close(stopChn)
		return err
	case sig := <-sysErr:
		log.Info().Msgf("terminating got [%v] signal", sig)
		return nil
	}
}

type DepositMessageStore struct {
	*redis.Client
}

type LevelDBTelemetry struct {
	store *DepositMessageStore
}

func (t *LevelDBTelemetry) TrackDepositMessage(m *message.Message) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	t.store.Set(context.Background(), fmt.Sprintf("%d%d", m.Source, m.DepositNonce), b, time.Duration(0)).Result()
}
