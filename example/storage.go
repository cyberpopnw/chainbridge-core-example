package example

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/bridge"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc1155"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc20"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc721"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmclient"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmgaspricer"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmtransaction"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/transactor/signAndSend"
	"github.com/ChainSafe/chainbridge-core/config"
	"github.com/ChainSafe/chainbridge-core/config/chain"
	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/ChainSafe/chainbridge-core/relayer/message"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/go-redis/redis/v8"
	"github.com/spf13/viper"
)

type EvmContracts struct {
	bridge         *bridge.BridgeContract
	erc1155Handler *erc1155.ERC1155HandlerContract
	erc20Handler   *erc20.ERC20HandlerContract
	erc721Handler  *erc721.ERC721HandlerContract
}

func NewDepositMessageStorage(configuration config.Config) (*RedisStoreTelemetry, error) {
	opts, err := redis.ParseURL(viper.GetString(flags.MessageStoreFlagName))
	if err != nil {
		return nil, err
	}
	redisDB := redis.NewClient(opts)
	messagestore := DepositMessageStore{redisDB}
	telemetry := &RedisStoreTelemetry{store: &messagestore, registry: make(map[uint8]*EvmContracts)}
	telemetry.addEVMChains(configuration)
	return telemetry, nil
}

func (r *RedisStoreTelemetry) addEVMChains(configuration config.Config) {
	for _, chainConfig := range configuration.ChainConfigs {
		switch chainConfig["type"] {
		case "evm":
			{
				config, err := chain.NewEVMConfig(chainConfig)
				if err != nil {
					panic(err)
				}
				client, err := evmclient.NewEVMClient(config)
				if err != nil {
					panic(err)
				}
				gasPricer := evmgaspricer.NewLondonGasPriceClient(client, nil)
				t := signAndSend.NewSignAndSendTransactor(evmtransaction.NewTransaction, gasPricer, client)

				bridgeContract := bridge.NewBridgeContract(client, common.HexToAddress(config.Bridge), t)
				erc1155Handler := erc1155.NewERC1155HandlerContract(client, common.HexToAddress(config.Erc1155Handler), t)
				erc721Handler := erc721.NewERC721HandlerContract(client, common.HexToAddress(config.Erc721Handler), t)
				erc20Handler := erc20.NewERC20HandlerContract(client, common.HexToAddress(config.Erc20Handler), t)
				r.registry[*config.GeneralChainConfig.Id] = &EvmContracts{
					bridge:         bridgeContract,
					erc1155Handler: erc1155Handler,
					erc20Handler:   erc20Handler,
					erc721Handler:  erc721Handler,
				}
			}
		}
	}
}

type DepositMessageStore struct {
	*redis.Client
}

type RedisStoreTelemetry struct {
	store    *DepositMessageStore
	registry map[uint8]*EvmContracts
}

type DepositStatus struct {
	Sender                  common.Address
	Recipient               common.Address
	SourceTokenAddress      common.Address
	DestinationTokenAddress common.Address
	TokenID                 float64
	Amount                  float64
	Type                    message.TransferType
}

func (t *RedisStoreTelemetry) TrackDepositMessage(m *message.Message) {
	if status, err := t.convert(m); err == nil {
		b, err := json.Marshal(status)
		if err != nil {
			return
		}
		t.store.Set(context.Background(), fmt.Sprintf("%d%d", m.Source, m.DepositNonce), b, time.Duration(0)).Result()
	}
}

func (t *RedisStoreTelemetry) convert(m *message.Message) (status *DepositStatus, err error) {
	status = &DepositStatus{}
	status.Type = m.Type
	status.Sender = m.Sender
	switch m.Type {
	case message.SemiFungibleTransfer:
		num, err := parseFloat64(m.Payload[0])
		if err != nil {
			return nil, err
		}
		status.TokenID = num
		num, err = parseFloat64(m.Payload[1])
		if err != nil {
			return nil, err
		}
		status.Amount = num

		if addr, ok := m.Payload[2].([]byte); ok {
			status.Recipient = common.BytesToAddress(addr)
		}

		chain := t.registry[m.Source]
		res, err := chain.erc1155Handler.CallContract("_resourceIDToTokenContractAddress", m.ResourceId)
		if err != nil {
			return status, err
		}
		out := abi.ConvertType(res[0], new(common.Address)).(*common.Address)
		status.SourceTokenAddress = *out
		chain = t.registry[m.Destination]
		res, err = chain.erc1155Handler.CallContract("_resourceIDToTokenContractAddress", m.ResourceId)
		if err != nil {
			return status, err
		}
		out = abi.ConvertType(res[0], new(common.Address)).(*common.Address)
		status.DestinationTokenAddress = *out

	}
	return status, nil
}

func parseFloat64(payload interface{}) (float64, error) {
	// declare new float64 as return value
	var payloadAmountFloat float64

	// cast interface to byte slice
	amountByteSlice, ok := payload.([]byte)
	if !ok {
		err := errors.New("could not cast interface to byte slice")
		return payloadAmountFloat, err
	}

	// convert big int => float64
	// ignore accuracy (rounding)
	payloadAmountFloat, _ = new(big.Float).SetInt(big.NewInt(0).SetBytes(amountByteSlice)).Float64()

	return payloadAmountFloat, nil
}
