package example

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/bridge"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc1155"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc20"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/contracts/erc721"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmclient"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmgaspricer"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/evmtransaction"
	"github.com/ChainSafe/chainbridge-core/chains/evm/calls/transactor/signAndSend"
	"github.com/ChainSafe/chainbridge-core/chains/evm/voter"
	"github.com/ChainSafe/chainbridge-core/config"
	"github.com/ChainSafe/chainbridge-core/config/chain"
	"github.com/ChainSafe/chainbridge-core/flags"
	"github.com/ChainSafe/chainbridge-core/relayer/message"
	"github.com/ChainSafe/chainbridge-core/types"
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
				mh := voter.NewEVMMessageHandler(*bridgeContract)
				mh.RegisterMessageHandler(config.Erc20Handler, voter.ERC20MessageHandler)
				mh.RegisterMessageHandler(config.Erc721Handler, voter.ERC721MessageHandler)
				mh.RegisterMessageHandler(config.Erc1155Handler, voter.ERC1155MessageHandler)
				mh.RegisterMessageHandler(config.GenericHandler, voter.GenericMessageHandler)
				r.messageHandler = mh
			}
		}
	}
}

type DepositMessageStore struct {
	*redis.Client
}

type RedisStoreTelemetry struct {
	store          *DepositMessageStore
	messageHandler *voter.EVMMessageHandler
	registry       map[uint8]*EvmContracts
}

type DepositRecord struct {
	Source                  uint8
	Destination             uint8
	Sender                  common.Address
	Recipient               common.Address
	SourceTokenAddress      common.Address
	DestinationTokenAddress common.Address
	TokenID                 float64
	Amount                  float64
	Type                    message.TransferType
	DepositNonce            uint64
	DataHash                common.Hash
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

func (t *RedisStoreTelemetry) GetDepositStatus(record *DepositRecord) (message.ProposalStatus, error) {
	chain := t.registry[record.Source]
	res, err := chain.bridge.CallContract("getProposal", record.Source, record.DepositNonce, record.DataHash)
	if err != nil {
		return message.ProposalStatus{}, err
	}
	out := *abi.ConvertType(res[0], new(message.ProposalStatus)).(*message.ProposalStatus)
	return out, nil
}

func (t *RedisStoreTelemetry) convert(m *message.Message) (record *DepositRecord, err error) {
	record = &DepositRecord{}
	record.Type = m.Type
	record.Sender = m.Sender
	record.DepositNonce = m.DepositNonce
	proposal, err := t.messageHandler.HandleMessage(m)
	if err != nil {
		return record, err
	}
	record.DataHash = proposal.GetDataHash()
	switch m.Type {
	case message.FungibleTransfer:
		num, err := parseFloat64(m.Payload[0])
		if err != nil {
			return nil, err
		}
		record.Amount = num

		if addr, ok := m.Payload[1].([]byte); ok {
			record.Recipient = common.BytesToAddress(addr)
		}

		out, err := t.resourceIdToContractAddress(m.Source, m.ResourceId, func(ec *EvmContracts) *contracts.Contract { return &ec.erc20Handler.Contract })
		if err != nil {
			return record, err
		}
		record.SourceTokenAddress = *out
		out, err = t.resourceIdToContractAddress(m.Destination, m.ResourceId, func(ec *EvmContracts) *contracts.Contract { return &ec.erc20Handler.Contract })
		if err != nil {
			return record, err
		}
		record.DestinationTokenAddress = *out
	case message.NonFungibleTransfer:
		tokenId, err := parseFloat64(m.Payload[0])
		if err != nil {
			return nil, err
		}
		record.TokenID = tokenId

		if addr, ok := m.Payload[1].([]byte); ok {
			record.Recipient = common.BytesToAddress(addr)
		}

		out, err := t.resourceIdToContractAddress(m.Source, m.ResourceId, func(ec *EvmContracts) *contracts.Contract { return &ec.erc721Handler.Contract })
		if err != nil {
			return record, err
		}
		record.SourceTokenAddress = *out
		out, err = t.resourceIdToContractAddress(m.Destination, m.ResourceId, func(ec *EvmContracts) *contracts.Contract { return &ec.erc721Handler.Contract })
		if err != nil {
			return record, err
		}
		record.DestinationTokenAddress = *out

	case message.SemiFungibleTransfer:
		num, err := parseFloat64(m.Payload[0])
		if err != nil {
			return nil, err
		}
		record.TokenID = num
		num, err = parseFloat64(m.Payload[1])
		if err != nil {
			return nil, err
		}
		record.Amount = num

		if addr, ok := m.Payload[2].([]byte); ok {
			record.Recipient = common.BytesToAddress(addr)
		}

		out, err := t.resourceIdToContractAddress(m.Source, m.ResourceId, func(chain *EvmContracts) *contracts.Contract { return &chain.erc1155Handler.Contract })
		if err != nil {
			return record, err
		}
		record.SourceTokenAddress = *out
		out, err = t.resourceIdToContractAddress(m.Destination, m.ResourceId, func(chain *EvmContracts) *contracts.Contract { return &chain.erc1155Handler.Contract })
		if err != nil {
			return record, err
		}
		record.DestinationTokenAddress = *out
	}
	return record, nil
}

func (t *RedisStoreTelemetry) resourceIdToContractAddress(chainId uint8, resourceId types.ResourceID, getHandler func(*EvmContracts) *contracts.Contract) (*common.Address, error) {
	chain := t.registry[chainId]
	res, err := getHandler(chain).CallContract("_resourceIDToTokenContractAddress", resourceId)
	if err != nil {
		return nil, err
	}
	out := abi.ConvertType(res[0], new(common.Address)).(*common.Address)
	return out, nil
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
