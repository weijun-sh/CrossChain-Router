package iota

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/anyswap/CrossChain-Router/v3/common"
	"github.com/anyswap/CrossChain-Router/v3/log"
	"github.com/anyswap/CrossChain-Router/v3/params"
	"github.com/anyswap/CrossChain-Router/v3/router"
	"github.com/anyswap/CrossChain-Router/v3/tokens"
	"github.com/iotaledger/hive.go/serializer"
	iotago "github.com/iotaledger/iota.go/v2"
)

const (
	KeepAlive uint64 = 1000000
)

// BuildRawTransaction build raw tx
//
//nolint:funlen,gocyclo // ok
func (b *Bridge) BuildRawTransaction(args *tokens.BuildTxArgs) (rawTx interface{}, err error) {
	if !params.IsTestMode && args.ToChainID.String() != b.ChainConfig.ChainID {
		return nil, tokens.ErrToChainIDMismatch
	}
	if args.Input != nil {
		return nil, fmt.Errorf("forbid build raw swap tx with input data")
	}
	if args.From == "" {
		return nil, fmt.Errorf("forbid empty sender")
	}
	routerMPC, getMpcErr := router.GetRouterMPC(args.GetTokenID(), b.ChainConfig.ChainID)
	if getMpcErr != nil {
		return nil, getMpcErr
	}
	if !common.IsEqualIgnoreCase(args.From, routerMPC) {
		log.Error("build tx mpc mismatch", "have", args.From, "want", routerMPC)
		return nil, tokens.ErrSenderMismatch
	}

	mpcPubkey := router.GetMPCPublicKey(args.From)
	if mpcPubkey == "" {
		return nil, tokens.ErrMissMPCPublicKey
	}

	erc20SwapInfo := args.ERC20SwapInfo
	multichainToken := router.GetCachedMultichainToken(erc20SwapInfo.TokenID, args.ToChainID.String())
	if multichainToken == "" {
		log.Warn("get multichain token failed", "tokenID", erc20SwapInfo.TokenID, "chainID", args.ToChainID)
		return nil, tokens.ErrMissTokenConfig
	}

	var inputs []*iotago.ToBeSignedUTXOInput
	var outputs []*iotago.SigLockedSingleOutput
	mpcEdAddr := ConvertStringToAddress(args.From)
	if mpcEdAddr == nil {
		return nil, err
	}
	if token := b.GetTokenConfig(multichainToken); token == nil {
		return nil, tokens.ErrMissTokenConfig
	} else {
		if receiver, amount, err := b.getReceiverAndAmount(args, multichainToken); err != nil {
			return nil, err
		} else {
			args.SwapValue = amount // swapValue
			if balance, err := b.CheckBalance(mpcEdAddr, amount.Uint64()); err != nil {
				log.Warn("CheckBalance error", "balance", balance, "needAmount", amount)
				return nil, err
			}
			if _, err = b.initExtra(args); err != nil {
				return nil, err
			} else {
				if outPutIDs, err := b.GetOutPutIDs(mpcEdAddr); err != nil {
					return nil, err
				} else {
					if toEdAddr, err := Bech32ToEdAddr(receiver); err == nil {
						value := amount.Uint64()
						finish := false
						for _, outputID := range outPutIDs {
							if outPut, needValue, returnValue, err := b.GetOutPutByID(outputID, value, finish); err == nil {
								inputs = append(inputs, &iotago.ToBeSignedUTXOInput{Address: mpcEdAddr, Input: outPut})
								if needValue == 0 {
									if returnValue == 0 || returnValue >= KeepAlive {
										outputs = append(outputs, &iotago.SigLockedSingleOutput{Address: *toEdAddr, Amount: amount.Uint64()})
										if returnValue != 0 {
											outputs = append(outputs, &iotago.SigLockedSingleOutput{Address: mpcEdAddr, Amount: returnValue})
										}
										break
									} else {
										value = returnValue
										finish = true
									}
								} else {
									value = needValue
								}
							}
						}
					} else {
						return nil, err
					}
				}
			}
		}
	}

	indexationPayload := &iotago.Indexation{
		Index: []byte("swapIn"),
		Data:  []byte(args.SwapID),
	}

	if messageBuilder := BuildMessage(inputs, outputs, indexationPayload); messageBuilder == nil {
		return nil, tokens.ErrInputAndOutputLength
	} else {
		return messageBuilder, nil
	}
}

func NewMessageBuilder() *MessageBuilder {
	return &MessageBuilder{
		TransactionBuilder: iotago.NewTransactionBuilder(),
		Essence: &iotago.TransactionEssence{
			Inputs:  serializer.Serializables{},
			Outputs: serializer.Serializables{},
			Payload: nil,
		},
	}
}

func BuildMessage(inputs []*iotago.ToBeSignedUTXOInput, outputs []*iotago.SigLockedSingleOutput, indexationPayload *iotago.Indexation) *MessageBuilder {
	if len(inputs) < 1 || len(outputs) < 1 {
		return nil
	}
	messageBuilder := NewMessageBuilder()
	for _, input := range inputs {
		messageBuilder.TransactionBuilder.AddInput(input)
		messageBuilder.Essence.Inputs = append(messageBuilder.Essence.Inputs, input.Input)
	}
	for _, output := range outputs {
		messageBuilder.TransactionBuilder.AddOutput(output)
		messageBuilder.Essence.Outputs = append(messageBuilder.Essence.Outputs, output)
	}
	if indexationPayload != nil {
		messageBuilder.TransactionBuilder.AddIndexationPayload(indexationPayload)
		messageBuilder.Essence.Payload = indexationPayload
	}
	return messageBuilder
}

// GetTxBlockInfo impl NonceSetter interface
func (b *Bridge) GetOutPutIDs(addr *iotago.Ed25519Address) ([]iotago.OutputIDHex, error) {
	urls := append(b.GetGatewayConfig().APIAddress, b.GetGatewayConfig().APIAddressExt...)
	for _, url := range urls {
		if outPuts, err := GetOutPutIDs(url, addr); err == nil {
			return outPuts, nil
		} else {
			log.Error("GetOutPutIDs", "err", err)
		}
	}
	return nil, tokens.ErrGetOutPutIDs
}

func (b *Bridge) GetOutPutByID(id iotago.OutputIDHex, needValue uint64, finish bool) (*iotago.UTXOInput, uint64, uint64, error) {
	urls := append(b.GetGatewayConfig().APIAddress, b.GetGatewayConfig().APIAddressExt...)
	for _, url := range urls {
		if outPut, needValue, returnValue, err := GetOutPutByID(url, id.MustAsUTXOInput().ID(), needValue, finish); err == nil {
			return outPut, needValue, returnValue, nil
		} else {
			log.Error("GetOutPutByID", "err", err)
		}
	}
	return nil, 0, 0, tokens.ErrGetOutPutByID
}

// GetPoolNonce impl NonceSetter interface
func (b *Bridge) GetPoolNonce(address, _height string) (uint64, error) {
	return 0, nil
}

// GetSeq returns account tx sequence
func (b *Bridge) GetSeq(args *tokens.BuildTxArgs) (nonceptr *uint64, err error) {
	var nonce uint64

	if params.IsParallelSwapEnabled() {
		nonce, err = b.AllocateNonce(args)
		return &nonce, err
	}

	if params.IsAutoSwapNonceEnabled(b.ChainConfig.ChainID) { // increase automatically
		nonce = b.GetSwapNonce(args.From)
		return &nonce, nil
	}

	nonce, err = b.GetPoolNonce(args.From, "pending")
	if err != nil {
		return nil, err
	}
	nonce = b.AdjustNonce(args.From, nonce)
	return &nonce, nil
}

func (b *Bridge) getReceiverAndAmount(args *tokens.BuildTxArgs, multichainToken string) (receiver string, amount *big.Int, err error) {
	erc20SwapInfo := args.ERC20SwapInfo
	receiver = args.Bind
	if !b.IsValidAddress(receiver) {
		log.Warn("swapout to wrong receiver", "receiver", args.Bind)
		return receiver, amount, errors.New("swapout to invalid receiver")
	}
	fromBridge := router.GetBridgeByChainID(args.FromChainID.String())
	if fromBridge == nil {
		return receiver, amount, tokens.ErrNoBridgeForChainID
	}
	fromTokenCfg := fromBridge.GetTokenConfig(erc20SwapInfo.Token)
	if fromTokenCfg == nil {
		log.Warn("get token config failed", "chainID", args.FromChainID, "token", erc20SwapInfo.Token)
		return receiver, amount, tokens.ErrMissTokenConfig
	}
	toTokenCfg := b.GetTokenConfig(multichainToken)
	if toTokenCfg == nil {
		return receiver, amount, tokens.ErrMissTokenConfig
	}
	amount = tokens.CalcSwapValue(erc20SwapInfo.TokenID, args.FromChainID.String(), b.ChainConfig.ChainID, args.OriginValue, fromTokenCfg.Decimals, toTokenCfg.Decimals, args.OriginFrom, args.OriginTxTo)
	return receiver, amount, err
}

func (b *Bridge) initExtra(args *tokens.BuildTxArgs) (extra *tokens.AllExtras, err error) {
	extra = args.Extra
	if extra == nil {
		extra = &tokens.AllExtras{}
		args.Extra = extra
	}
	if extra.Sequence == nil {
		extra.Sequence, err = b.GetSeq(args)
		if err != nil {
			return nil, err
		}
	}
	return extra, nil
}
