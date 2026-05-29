// Command pebble-poc is a throwaway Milestone 0 benchmark used to validate
// Pebble's behavior before committing to it as the Toise event-log store.
//
// It opens a fresh Pebble database, writes a number of realistically-sized
// dummy "events" in batches (measuring per-batch commit latency), reads them
// all back, and reports write/read throughput plus on-disk size. Results feed
// ADR 0016 (pebble-validation).
//
// This program lives in its own Go module so it does not add Pebble to the
// main module's dependencies before Milestone 2 formally adopts it.
package main

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cockroachdb/pebble"
)

const (
	numEvents = 10_000
	batchSize = 100
	valueSize = 256 // bytes; a plausible serialized entity event
)

func main() {
	dir, err := os.MkdirTemp("", "pebble-poc-*")
	if err != nil {
		fatal("mktemp", err)
	}
	defer os.RemoveAll(dir)

	db, err := pebble.Open(dir, &pebble.Options{})
	if err != nil {
		fatal("open", err)
	}

	value := make([]byte, valueSize)
	for i := range value {
		value[i] = byte('a' + i%26)
	}

	// --- write 10k events in batches of 100, syncing each batch ---
	batchLatencies := make([]time.Duration, 0, numEvents/batchSize)
	writeStart := time.Now()
	for i := 0; i < numEvents; i += batchSize {
		b := db.NewBatch()
		for j := 0; j < batchSize && i+j < numEvents; j++ {
			key := make([]byte, 8)
			binary.BigEndian.PutUint64(key, uint64(i+j))
			if err := b.Set(key, value, nil); err != nil {
				fatal("batch set", err)
			}
		}
		bs := time.Now()
		if err := b.Commit(pebble.Sync); err != nil {
			fatal("commit", err)
		}
		batchLatencies = append(batchLatencies, time.Since(bs))
		if err := b.Close(); err != nil {
			fatal("batch close", err)
		}
	}
	writeDur := time.Since(writeStart)

	// --- read everything back via a full scan ---
	readStart := time.Now()
	count := 0
	iter, err := db.NewIter(nil)
	if err != nil {
		fatal("new iter", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		_ = iter.Key()
		_ = iter.Value()
		count++
	}
	if err := iter.Close(); err != nil {
		fatal("iter close", err)
	}
	readDur := time.Since(readStart)

	if err := db.Flush(); err != nil {
		fatal("flush", err)
	}
	onDisk := dirSize(dir)
	if err := db.Close(); err != nil {
		fatal("close", err)
	}

	sort.Slice(batchLatencies, func(a, b int) bool { return batchLatencies[a] < batchLatencies[b] })
	p50 := batchLatencies[len(batchLatencies)*50/100]
	p99 := batchLatencies[len(batchLatencies)*99/100]
	maxLat := batchLatencies[len(batchLatencies)-1]

	fmt.Println("=== Pebble PoC (Milestone 0) ===")
	fmt.Printf("events written       : %d (batches of %d, Sync)\n", numEvents, batchSize)
	fmt.Printf("events read back     : %d\n", count)
	fmt.Printf("write duration       : %s\n", writeDur.Round(time.Millisecond))
	fmt.Printf("write throughput     : %.0f evts/s\n", float64(numEvents)/writeDur.Seconds())
	fmt.Printf("batch commit p50     : %s\n", p50.Round(10*time.Microsecond))
	fmt.Printf("batch commit p99     : %s\n", p99.Round(10*time.Microsecond))
	fmt.Printf("batch commit max     : %s\n", maxLat.Round(10*time.Microsecond))
	fmt.Printf("read duration (scan) : %s\n", readDur.Round(time.Millisecond))
	fmt.Printf("read throughput      : %.0f evts/s\n", float64(count)/readDur.Seconds())
	fmt.Printf("on-disk size         : %.2f MB (%d bytes, %.1f bytes/event)\n",
		float64(onDisk)/(1024*1024), onDisk, float64(onDisk)/float64(numEvents))
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func fatal(stage string, err error) {
	fmt.Fprintf(os.Stderr, "pebble-poc: %s: %v\n", stage, err)
	os.Exit(1)
}
