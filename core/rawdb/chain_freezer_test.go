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

package rawdb

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/ethdb"
)

// chainFreezerLockTimeout bounds how long a test waits for an operation that
// is expected to proceed. It is generous so a slow CI runner does not flake.
const chainFreezerLockTimeout = 5 * time.Second

// chainFreezerBlockedWait is how long a test waits to confirm that an
// operation is held back by the freezer lock.
const chainFreezerBlockedWait = 200 * time.Millisecond

// newTestChainFreezer opens a file-backed chain freezer (backed by *Freezer,
// the only store whose lock chainFreezer.ReadAncients takes) and writes one
// item into every table.
func newTestChainFreezer(t *testing.T) *chainFreezer {
	t.Helper()
	f, err := newChainFreezer(t.TempDir(), "", "", false)
	if err != nil {
		t.Fatalf("open chain freezer: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	if _, ok := f.ancients.(*Freezer); !ok {
		t.Fatalf("chain freezer is backed by %T, want *Freezer", f.ancients)
	}
	_, err = f.ModifyAncients(func(op ethdb.AncientWriteOp) error {
		for kind := range chainFreezerTableConfigs {
			if err := op.AppendRaw(kind, 0, []byte{0x01}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed chain freezer: %v", err)
	}
	return f
}

// holdReadAncients starts a ReadAncients call in a new goroutine and keeps it
// inside fn until release is closed. It returns once fn is running, and the
// returned channel yields ReadAncients' error when it ends.
func holdReadAncients(t *testing.T, f *chainFreezer, release <-chan struct{}) <-chan error {
	t.Helper()
	var (
		entered = make(chan struct{})
		done    = make(chan error, 1)
	)
	go func() {
		done <- f.ReadAncients(func(op ethdb.AncientReaderOp) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(chainFreezerLockTimeout):
		t.Fatal("first ReadAncients never entered its read operation")
	}
	return done
}

// TestChainFreezerReadAncientsConcurrent checks that two ReadAncients calls on
// a file-backed chain freezer run at the same time: a reader does not wait
// for another reader to finish.
func TestChainFreezerReadAncientsConcurrent(t *testing.T) {
	f := newTestChainFreezer(t)

	release := make(chan struct{})
	firstDone := holdReadAncients(t, f, release)

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- f.ReadAncients(func(op ethdb.AncientReaderOp) error {
			blob, err := op.Ancient(ChainFreezerHeaderTable, 0)
			if err != nil {
				return err
			}
			if !bytes.Equal(blob, []byte{0x01}) {
				t.Errorf("header 0 = %x, want 01", blob)
			}
			return nil
		})
	}()
	var secondErr error
	select {
	case secondErr = <-secondDone:
	case <-time.After(chainFreezerLockTimeout):
		t.Error("second ReadAncients blocked while the first was still reading: readers are serialised")
		// Unblock the first reader so the second can finish and no
		// goroutine outlives the test.
		close(release)
		release = nil
		secondErr = <-secondDone
	}
	if release != nil {
		close(release)
	}
	if err := <-firstDone; err != nil {
		t.Errorf("first ReadAncients: %v", err)
	}
	if secondErr != nil {
		t.Errorf("second ReadAncients: %v", secondErr)
	}
}

// TestChainFreezerWriterExcludesReaders checks that a writer still waits for
// an in-flight ReadAncients call, and that ReadAncients waits for an
// in-flight writer.
func TestChainFreezerWriterExcludesReaders(t *testing.T) {
	writers := map[string]func(f *chainFreezer) error{
		"ModifyAncients": func(f *chainFreezer) error {
			_, err := f.ModifyAncients(func(op ethdb.AncientWriteOp) error {
				for kind := range chainFreezerTableConfigs {
					if err := op.AppendRaw(kind, 1, []byte{0x02}); err != nil {
						return err
					}
				}
				return nil
			})
			return err
		},
		"TruncateHead": func(f *chainFreezer) error {
			_, err := f.TruncateHead(0)
			return err
		},
		"TruncateTail": func(f *chainFreezer) error {
			_, err := f.TruncateTail(1)
			return err
		},
	}
	for name, write := range writers {
		t.Run(name+"WaitsForReader", func(t *testing.T) {
			f := newTestChainFreezer(t)

			release := make(chan struct{})
			readDone := holdReadAncients(t, f, release)

			writeDone := make(chan error, 1)
			go func() { writeDone <- write(f) }()

			select {
			case <-writeDone:
				close(release)
				<-readDone
				t.Fatalf("%s finished while ReadAncients was still reading", name)
			case <-time.After(chainFreezerBlockedWait):
			}
			close(release)
			if err := <-readDone; err != nil {
				t.Fatalf("ReadAncients: %v", err)
			}
			select {
			case err := <-writeDone:
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			case <-time.After(chainFreezerLockTimeout):
				t.Fatalf("%s never finished after the reader left", name)
			}
		})
	}

	t.Run("ReaderWaitsForModifyAncients", func(t *testing.T) {
		f := newTestChainFreezer(t)

		var (
			entered   = make(chan struct{})
			release   = make(chan struct{})
			writeDone = make(chan error, 1)
		)
		go func() {
			_, err := f.ModifyAncients(func(op ethdb.AncientWriteOp) error {
				close(entered)
				<-release
				return nil
			})
			writeDone <- err
		}()
		select {
		case <-entered:
		case <-time.After(chainFreezerLockTimeout):
			t.Fatal("ModifyAncients never entered its write operation")
		}

		readDone := make(chan error, 1)
		go func() {
			readDone <- f.ReadAncients(func(op ethdb.AncientReaderOp) error { return nil })
		}()
		select {
		case <-readDone:
			close(release)
			<-writeDone
			t.Fatal("ReadAncients ran while ModifyAncients was still writing")
		case <-time.After(chainFreezerBlockedWait):
		}
		close(release)
		if err := <-writeDone; err != nil {
			t.Fatalf("ModifyAncients: %v", err)
		}
		select {
		case err := <-readDone:
			if err != nil {
				t.Fatalf("ReadAncients: %v", err)
			}
		case <-time.After(chainFreezerLockTimeout):
			t.Fatal("ReadAncients never finished after the writer left")
		}
	})
}
