// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package native_test

import (
	"encoding/json"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/arbitrum/multigas"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// rejectedTxProcessor models the Nitro hook contract: reject execution while
// consuming gas and advancing the sender nonce. The state transition and tracer
// are real; this seam avoids needing a running Nitro node or its filter state.
type rejectedTxProcessor struct {
	vm.TxProcessingHook
	state *state.StateDB
	from  common.Address
	err   error
}

func (p rejectedTxProcessor) RevertedTxHook(gas *uint64, used multigas.MultiGas) (multigas.MultiGas, error) {
	p.state.SetNonce(p.from, p.state.GetNonce(p.from)+1, tracing.NonceChangeEoACall)
	used = used.SaturatingAdd(multigas.ComputationGas(*gas))
	*gas = 0
	return used, p.err
}

// A rejection before EVM Call/Create must still yield a failed root trace.
// Losing the scope hooks here used to make callTracer reject the whole result
// with "incorrect number of top-level calls".
func TestRevertedTxHookTrace(t *testing.T) {
	from := common.HexToAddress("0xaaaa")
	to := common.HexToAddress("0xbbbb")
	for _, rejection := range []struct {
		name string
		err  error
	}{
		{"recorded_revert", vm.ErrExecutionReverted},
		{"filtered", &core.ErrFilteredTx{TxHash: common.HexToHash("0x1234")}},
	} {
		for _, create := range []bool{false, true} {
			for _, name := range []string{"callTracer", "flatCallTracer", "erc7562Tracer"} {
				t.Run(fmt.Sprintf("%s/%s/create=%t", rejection.name, name, create), func(t *testing.T) {
					var target *common.Address
					if !create {
						target = &to
					}
					tx := types.NewTx(&types.DynamicFeeTx{To: target, Gas: 153090, GasFeeCap: big.NewInt(2), GasTipCap: big.NewInt(2), Value: big.NewInt(7), Data: []byte{byte(vm.STOP)}})
					tracer, err := tracers.DefaultDirectory.New(name, &tracers.Context{}, nil, params.TestChainConfig)
					require.NoError(t, err)
					db, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
					require.NoError(t, err)
					db.SetBalance(from, uint256.NewInt(1000000), tracing.BalanceChangeUnspecified)
					// This would write storage if the rejected call were executed.
					db.SetCode(to, []byte{byte(vm.PUSH1), 1, byte(vm.PUSH1), 0, byte(vm.SSTORE), byte(vm.STOP)}, tracing.CodeChangeUnspecified)
					header := &types.Header{Number: common.Big0, Difficulty: common.Big0, BaseFee: common.Big0}
					evm := vm.NewEVM(core.NewEVMBlockContext(header, nil, &common.Address{}), db, params.TestChainConfig, vm.Config{Tracer: tracer.Hooks})
					evm.ProcessingHook = rejectedTxProcessor{evm.ProcessingHook, db, from, rejection.err}
					msg := &core.Message{Tx: tx, From: from, To: target, GasLimit: tx.Gas(), Value: tx.Value(), Data: tx.Data(), GasPrice: big.NewInt(2), GasFeeCap: tx.GasFeeCap(), GasTipCap: tx.GasTipCap()}
					var usedGas uint64
					receipt, result, err := core.ApplyTransactionWithEVM(msg, new(core.GasPool).AddGas(tx.Gas()), db, common.Big0, common.Hash{}, 0, tx, &usedGas, evm, nil)
					require.NoError(t, err)
					require.ErrorIs(t, result.Err, rejection.err)
					require.Equal(t, uint64(types.ReceiptStatusFailed), receipt.Status)
					require.Equal(t, uint64(153090), receipt.GasUsed)
					require.Equal(t, uint64(1), db.GetNonce(from))
					require.Equal(t, uint64(693820), db.GetBalance(from).Uint64()) // 1000000 - 153090 * 2; no value transfer
					require.Equal(t, uint64(306180), db.GetBalance(common.Address{}).Uint64())
					require.Zero(t, db.GetBalance(to).Uint64())
					require.Equal(t, common.Hash{}, db.GetState(to, common.Hash{}))
					require.Empty(t, receipt.Logs)
					require.Nil(t, result.TopLevelDeployed)
					raw, err := tracer.GetResult()
					require.NoError(t, err)
					if name == "flatCallTracer" {
						var frames []struct {
							Error     string
							Result    json.RawMessage
							Subtraces int
						}
						require.NoError(t, json.Unmarshal(raw, &frames))
						require.Len(t, frames, 1)
						require.NotEmpty(t, frames[0].Error)
						// Flat traces retain gas/output for REVERT, but omit the result
						// for other failures. Preserve that existing output contract.
						if rejection.err == vm.ErrExecutionReverted {
							var result struct {
								GasUsed hexutil.Uint64
								Address *common.Address
								Code    hexutil.Bytes
							}
							require.NoError(t, json.Unmarshal(frames[0].Result, &result))
							require.Equal(t, uint64(153090), uint64(result.GasUsed))
							require.Nil(t, result.Address)
							require.Empty(t, result.Code)
						} else {
							require.Empty(t, frames[0].Result)
						}
						require.Zero(t, frames[0].Subtraces)
					} else {
						var frame struct {
							Type         string
							From         common.Address
							To           *common.Address
							Input        hexutil.Bytes
							Gas, GasUsed hexutil.Uint64
							Value        *hexutil.Big
							Error        string
							Calls        []json.RawMessage
						}
						require.NoError(t, json.Unmarshal(raw, &frame))
						require.Equal(t, rejection.err.Error(), frame.Error)
						require.Equal(t, from, frame.From)
						require.Equal(t, tx.Data(), []byte(frame.Input))
						require.Equal(t, uint64(153090), uint64(frame.Gas))
						require.Equal(t, uint64(153090), uint64(frame.GasUsed))
						require.Equal(t, int64(7), (*big.Int)(frame.Value).Int64())
						require.Empty(t, frame.Calls)
						if create {
							require.Equal(t, "CREATE", frame.Type)
							require.Nil(t, frame.To)
						} else {
							require.Equal(t, "CALL", frame.Type)
							require.Equal(t, &to, frame.To)
						}
					}
				})
			}
		}
	}
}
