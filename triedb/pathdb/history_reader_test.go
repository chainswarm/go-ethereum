// Copyright 2025 The go-ethereum Authors
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
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/

package pathdb

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/internal/testrand"
)

func waitIndexing(db *Database) {
	for {
		metadata := loadIndexMetadata(db.diskdb, typeStateHistory)
		if metadata != nil && metadata.Last >= db.tree.bottom().stateID() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func stateAvail(id uint64, env *tester) bool {
	if env.db.config.StateHistory == 0 {
		return true
	}
	dl := env.db.tree.bottom()
	if dl.stateID() <= env.db.config.StateHistory {
		return true
	}
	firstID := dl.stateID() - env.db.config.StateHistory + 1

	return id+1 >= firstID
}

func checkHistoricalState(env *tester, root common.Hash, id uint64, hr *historyReader) error {
	if !stateAvail(id, env) {
		return nil
	}

	// Short circuit if the historical state is no longer available
	if rawdb.ReadStateID(env.db.diskdb, root) == nil {
		return fmt.Errorf("state not found %d %x", id, root)
	}

	var (
		dl       = env.db.tree.bottom()
		stateID  = rawdb.ReadStateID(env.db.diskdb, root)
		accounts = env.snapAccounts[root]
		storages = env.snapStorages[root]
	)
	for addrHash, accountData := range accounts {
		latest, _ := dl.account(addrHash, 0)
		blob, err := hr.read(newAccountIdentQuery(env.accountPreimage(addrHash), addrHash), *stateID, dl.stateID(), latest)
		if err != nil {
			return err
		}
		if !bytes.Equal(accountData, blob) {
			return fmt.Errorf("wrong account data, expected %x, got %x", accountData, blob)
		}
	}
	for i := 0; i < len(env.roots); i++ {
		if env.roots[i] == root {
			break
		}
		// Find all accounts deleted in the past, ensure the associated data is null
		for addrHash := range env.snapAccounts[env.roots[i]] {
			if _, ok := accounts[addrHash]; !ok {
				latest, _ := dl.account(addrHash, 0)
				blob, err := hr.read(newAccountIdentQuery(env.accountPreimage(addrHash), addrHash), *stateID, dl.stateID(), latest)
				if err != nil {
					return err
				}
				if len(blob) != 0 {
					return fmt.Errorf("wrong account data, expected null, got %x", blob)
				}
			}
		}
	}
	for addrHash, slots := range storages {
		for slotHash, slotData := range slots {
			latest, _ := dl.storage(addrHash, slotHash, 0)
			blob, err := hr.read(newStorageIdentQuery(env.accountPreimage(addrHash), addrHash, env.hashPreimage(slotHash), slotHash), *stateID, dl.stateID(), latest)
			if err != nil {
				return err
			}
			if !bytes.Equal(slotData, blob) {
				return fmt.Errorf("wrong storage data, expected %x, got %x", slotData, blob)
			}
		}
	}
	for i := 0; i < len(env.roots); i++ {
		if env.roots[i] == root {
			break
		}
		// Find all storage slots deleted in the past, ensure the associated data is null
		for addrHash, slots := range env.snapStorages[env.roots[i]] {
			for slotHash := range slots {
				_, ok := storages[addrHash]
				if ok {
					_, ok = storages[addrHash][slotHash]
				}
				if !ok {
					latest, _ := dl.storage(addrHash, slotHash, 0)
					blob, err := hr.read(newStorageIdentQuery(env.accountPreimage(addrHash), addrHash, env.hashPreimage(slotHash), slotHash), *stateID, dl.stateID(), latest)
					if err != nil {
						return err
					}
					if len(blob) != 0 {
						return fmt.Errorf("wrong storage data, expected null, got %x", blob)
					}
				}
			}
		}
	}
	return nil
}

func TestHistoryReader(t *testing.T) {
	testHistoryReader(t, 0)  // with all histories reserved
	testHistoryReader(t, 10) // with latest 10 histories reserved
}

func testHistoryReader(t *testing.T, historyLimit uint64) {
	//log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelDebug, true)))
	config := &testerConfig{
		stateHistory:  historyLimit,
		layers:        64,
		maxDiffLayers: 4,
		enableIndex:   true,
	}
	env := newTester(t, config)
	defer env.release()
	waitIndexing(env.db)

	var (
		roots = env.roots
		dl    = env.db.tree.bottom()
		hr    = newHistoryReader(env.db.diskdb, env.db.stateFreezer)
	)
	for i, root := range roots {
		if root == dl.rootHash() {
			break
		}
		if err := checkHistoricalState(env, root, uint64(i+1), hr); err != nil {
			t.Fatal(err)
		}
	}

	// Pile up more histories on top, ensuring the historic reader is not affected
	env.extend(4)
	waitIndexing(env.db)

	for i, root := range roots {
		if root == dl.rootHash() {
			break
		}
		if err := checkHistoricalState(env, root, uint64(i+1), hr); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoricalStateReader(t *testing.T) {
	//log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelDebug, true)))
	config := &testerConfig{
		stateHistory:  0,
		layers:        64,
		maxDiffLayers: 4,
		enableIndex:   true,
	}
	env := newTester(t, config)
	defer env.release()
	waitIndexing(env.db)

	// non-canonical state
	fakeRoot := testrand.Hash()
	rawdb.WriteStateID(env.db.diskdb, fakeRoot, 10)

	_, err := env.db.HistoricReader(fakeRoot)
	if err == nil {
		t.Fatal("expected error")
	}
	t.Log(err)

	// canonical state
	realRoot := env.roots[9]
	_, err = env.db.HistoricReader(realRoot)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
}

// A syncing node replaces the bottom disk layer about once per block, and a
// parallel range trace issues thousands of historic reads per block, so a
// reader can resolve a bottom layer that goes stale before it reads it. The
// stale error used to escape to core/state, which swallows it into a zero
// value — the 2026-09-13 nitro-robinhood-0 divide-by-zero crash loop. The
// reader must re-resolve the bottom layer and return the real value.
func TestHistoricalStateReaderRetriesStaleBottom(t *testing.T) {
	config := &testerConfig{
		stateHistory:  0,
		layers:        64,
		maxDiffLayers: 4,
		enableIndex:   true,
	}
	env := newTester(t, config)
	defer env.release()
	waitIndexing(env.db)

	root := env.roots[9]
	reader, err := env.db.HistoricReader(root)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	live := env.db.tree.bottom()
	stale := newDiskLayer(live.root, live.id, live.db, live.nodes, live.states, live.buffer, live.frozen)
	stale.markStale()
	if _, err := stale.account(common.Hash{}, 0); !errors.Is(err, errSnapshotStale) {
		t.Fatalf("injected layer must read as stale, got %v", err)
	}

	var (
		addrHash common.Hash
		want     []byte
	)
	for hash, data := range env.snapAccounts[root] {
		if len(data) > 0 {
			addrHash, want = hash, data
			break
		}
	}
	if want == nil {
		t.Fatal("no account in fixture")
	}
	addr := env.accountPreimage(addrHash)

	// Stale on the first resolve, live afterwards: the read must succeed with
	// the historical value, not the stale error or a zero.
	calls := 0
	reader.bottom = func() *diskLayer {
		calls++
		if calls == 1 {
			return stale
		}
		return env.db.tree.bottom()
	}
	before := historicalStaleRetryMeter.Snapshot().Count()
	got, err := reader.AccountRLP(addr)
	if err != nil {
		t.Fatalf("stale bottom must be retried, got error: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("wrong account after retry: got %x want %x", got, want)
	}
	if calls != 2 {
		t.Fatalf("expected exactly one retry, resolved bottom %d times", calls)
	}
	if delta := historicalStaleRetryMeter.Snapshot().Count() - before; delta != 1 {
		t.Fatalf("retry meter delta = %d, want 1", delta)
	}

	// Storage takes the same path.
	var (
		slotHash common.Hash
		slotWant []byte
	)
	for hash, data := range env.snapStorages[root][addrHash] {
		if len(data) > 0 {
			slotHash, slotWant = hash, data
			break
		}
	}
	if slotWant != nil {
		calls = 0
		got, err := reader.Storage(addr, env.hashPreimage(slotHash))
		if err != nil {
			t.Fatalf("stale bottom must be retried for storage, got error: %v", err)
		}
		if !bytes.Equal(got, slotWant) {
			t.Fatalf("wrong storage after retry: got %x want %x", got, slotWant)
		}
		if calls != 2 {
			t.Fatalf("expected exactly one storage retry, resolved bottom %d times", calls)
		}
	}

	// A layer that never stops being stale must still fail, bounded, not spin.
	calls = 0
	reader.bottom = func() *diskLayer { calls++; return stale }
	if _, err := reader.AccountRLP(addr); !errors.Is(err, errSnapshotStale) {
		t.Fatalf("permanently stale bottom must surface errSnapshotStale, got %v", err)
	}
	if calls != historicStaleRetries+1 {
		t.Fatalf("expected %d bounded attempts, got %d", historicStaleRetries+1, calls)
	}
}
