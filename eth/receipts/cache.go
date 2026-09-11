// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at your
// option) any later version.

// Package receipts contains interfaces shared by receipt-serving APIs.
package receipts

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
)

// Cache persists the JSON response for eth_getBlockReceipts, keyed by block
// identity. Implementations must be safe for concurrent use. A nil cache
// keeps the stock live-read behavior.
//
// Get returns nil on a miss, stale identity, or invalid entry. A non-nil
// result may contain JSON null or an empty array; callers must not treat those
// values as misses.
type Cache interface {
	Get(blockHash common.Hash, number uint64) json.RawMessage
	Put(blockHash common.Hash, number uint64, result json.RawMessage)
}
