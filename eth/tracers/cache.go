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

package tracers

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
)

// TraceCache persists callTracer block traces, keyed by block hash.
// Implementations must be safe for concurrent use. A backend whose
// TraceCache() returns nil keeps stock behavior: every trace re-executes.
//
// Entries are the exact json.Marshal output of the []*txTraceResult the
// single-block debug_traceBlockByNumber call returns, so a hit can be
// served verbatim — byte-identical to re-execution.
type TraceCache interface {
	// Get returns the cached marshaled trace result for the block, or nil
	// on miss, stale hash, or corruption. Errors never propagate: any
	// unreadable entry is a miss.
	Get(blockHash common.Hash, number uint64) json.RawMessage
	// Put stores the marshaled trace result for the block.
	Put(blockHash common.Hash, number uint64, results json.RawMessage)
}
