// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.

package ethapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/receipts"
	"github.com/ethereum/go-ethereum/rpc"
)

type memoryReceiptCache struct {
	values map[common.Hash]json.RawMessage
	gets   int
	puts   int
}

func newMemoryReceiptCache() *memoryReceiptCache {
	return &memoryReceiptCache{values: make(map[common.Hash]json.RawMessage)}
}

func (c *memoryReceiptCache) Get(hash common.Hash, _ uint64) json.RawMessage {
	c.gets++
	return c.values[hash]
}

func (c *memoryReceiptCache) Put(hash common.Hash, _ uint64, result json.RawMessage) {
	c.puts++
	c.values[hash] = append(json.RawMessage(nil), result...)
}

type receiptCacheTestBackend struct {
	*testBackend
	cache       receipts.Cache
	getReceipts int
}

func (b *receiptCacheTestBackend) ReceiptCache() receipts.Cache { return b.cache }

func (b *receiptCacheTestBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	b.getReceipts++
	return b.testBackend.GetReceipts(ctx, hash)
}

func TestGetBlockReceiptsReadThroughAndFallback(t *testing.T) {
	backend, _ := setupReceiptBackend(t, 6)
	cache := newMemoryReceiptCache()
	wrapped := &receiptCacheTestBackend{testBackend: backend, cache: cache}
	api := NewBlockChainAPI(wrapped)
	ctx := context.Background()
	request := rpc.BlockNumberOrHashWithNumber(2)

	live, err := NewBlockChainAPI(backend).GetBlockReceipts(ctx, request)
	if err != nil {
		t.Fatalf("live GetBlockReceipts: %v", err)
	}
	got, err := api.GetBlockReceipts(ctx, request)
	if err != nil {
		t.Fatalf("cached miss GetBlockReceipts: %v", err)
	}
	if cache.puts != 1 || wrapped.getReceipts != 1 {
		t.Fatalf("miss bookkeeping = puts %d live reads %d, want 1/1", cache.puts, wrapped.getReceipts)
	}
	assertJSONEqual(t, live, got)

	got, err = api.GetBlockReceipts(ctx, request)
	if err != nil {
		t.Fatalf("cached hit GetBlockReceipts: %v", err)
	}
	if wrapped.getReceipts != 1 {
		t.Fatalf("cache hit performed %d live reads, want 1", wrapped.getReceipts)
	}
	assertJSONEqual(t, live, got)

	block, err := backend.BlockByNumber(ctx, rpc.BlockNumber(2))
	if err != nil {
		t.Fatalf("BlockByNumber: %v", err)
	}
	cache.values[block.Hash()] = json.RawMessage(`{"not":"receipts"}`)
	if _, err := api.GetBlockReceipts(ctx, request); err != nil {
		t.Fatalf("malformed cached payload must fall back: %v", err)
	}
	if wrapped.getReceipts != 2 {
		t.Fatalf("malformed cache performed %d live reads, want 2", wrapped.getReceipts)
	}
}

func TestGetBlockReceiptsCachesEmptyResult(t *testing.T) {
	backend, _ := setupReceiptBackend(t, 1)
	cache := newMemoryReceiptCache()
	wrapped := &receiptCacheTestBackend{testBackend: backend, cache: cache}
	api := NewBlockChainAPI(wrapped)
	request := rpc.BlockNumberOrHashWithNumber(0)

	first, err := api.GetBlockReceipts(context.Background(), request)
	if err != nil {
		t.Fatalf("empty live result: %v", err)
	}
	if first == nil || len(first) != 0 {
		t.Fatalf("empty result = %#v, want non-nil empty slice", first)
	}
	if got := string(cache.values[backend.chain.GetBlockByNumber(0).Hash()]); got != "[]" {
		t.Fatalf("empty result cache bytes = %q, want []", got)
	}
	if _, err := api.GetBlockReceipts(context.Background(), request); err != nil {
		t.Fatalf("empty cached result: %v", err)
	}
	if wrapped.getReceipts != 1 {
		t.Fatalf("empty cache hit performed %d live reads, want 1", wrapped.getReceipts)
	}
}

func assertJSONEqual(t *testing.T, want, got interface{}) {
	t.Helper()
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	if string(wantJSON) != string(gotJSON) {
		t.Fatalf("JSON mismatch\nwant: %s\n got: %s", wantJSON, gotJSON)
	}
}
