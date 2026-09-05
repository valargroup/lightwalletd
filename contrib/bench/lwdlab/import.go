package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/btcsuite/btcd/rpcclient"
	"github.com/sirupsen/logrus"
	"github.com/zcash/lightwalletd/common"
	"github.com/zcash/lightwalletd/frontend"
	"github.com/zcash/lightwalletd/hash32"
	"github.com/zcash/lightwalletd/walletrpc"
)

// importCache prepares real cache bytes with the ordinary block parser and cache
// writer. Bounded parallel fetching is preparation, not a server benchmark.
// The server must be stopped before resuming an existing cache directory.
func importCache(args []string) {
	flags := flag.NewFlagSet("import-cache", flag.ExitOnError)
	dataDir := flags.String("data-dir", "", "private cache directory; no server may have it open")
	address := flags.String("rpc-address", "127.0.0.1:18232", "isolated mainnet node on loopback")
	tip := flags.Int("tip", 0, "required fixed node tip height")
	tipHash := flags.String("tip-hash", "", "required fixed node tip hash in display byte order")
	end := flags.Int("end", -1, "last cache height, default node tip")
	workers := flags.Int("workers", 8, "parallel block fetches (1-16)")
	resume := flags.Bool("resume", false, "resume an existing cache after stopping its server")
	flags.Parse(args)
	// Finish in-flight RPCs and cache writes before stopping for a benchmark.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGUSR1)
	defer signal.Stop(stop)
	if *dataDir == "" || *tip < 1 || *workers < 1 || *workers > 16 {
		fatalf("invalid import arguments")
	}
	if err := requireLoopback(*address); err != nil {
		fatalf("%v", err)
	}
	expected, err := hex.DecodeString(*tipHash)
	if err != nil || len(expected) != 32 {
		fatalf("expected a 32-byte tip hash")
	}
	if *end < 0 {
		*end = *tip
	}
	if *end > *tip {
		fatalf("cache end exceeds node tip")
	}
	if _, err := os.Stat(*dataDir); err == nil && !*resume {
		fatalf("directory exists; explicit -resume required")
	}
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		fatalf("%v", err)
	}
	lockPath := filepath.Join(*dataDir, "import.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fatalf("another import may be active: %v", err)
	}
	lock.Close()
	defer os.Remove(lockPath)
	logger := logrus.New()
	logger.SetOutput(os.Stderr)
	common.Log = logger.WithField("component", "mainnet-cache-import")
	common.RawRequest, err = frontend.NewContextRawRequest(&rpcclient.ConnConfig{
		Host: *address, User: "benchmark", Pass: "unused", DisableTLS: true, HTTPPostMode: true,
	})
	if err != nil {
		fatalf("%v", err)
	}
	checkTip := func() error {
		body, err := common.RawRequest(context.Background(), "getblockchaininfo", nil)
		if err != nil {
			return err
		}
		var state struct {
			Chain         string
			Blocks        int
			BestBlockHash string
		}
		if err := json.Unmarshal(body, &state); err != nil {
			return err
		}
		if state.Chain != "main" || state.Blocks != *tip || state.BestBlockHash != *tipHash {
			return fmt.Errorf("node does not match pinned mainnet state")
		}
		return nil
	}
	if err := checkTip(); err != nil {
		fatalf("%v", err)
	}
	cache := common.NewBlockCache(filepath.Join(*dataDir, "db"), "main", 0, -1)
	defer cache.Close()
	first := cache.GetLatestHeight() + 1
	if first > *end+1 {
		fatalf("existing cache extends beyond the requested end")
	}
	var previous []byte
	if first > 0 {
		last := cache.Get(first - 1)
		actual, err := common.GetBlock(context.Background(), nil, first-1)
		if err != nil || last == nil || actual == nil || !bytes.Equal(last.Hash, actual.Hash) {
			fatalf("existing cache tip does not match node: %v", err)
		}
		previous = last.Hash
	}
	started := time.Now()
	for next := first; next <= *end; {
		select {
		case <-stop:
			cache.Sync()
			fmt.Fprintf(os.Stderr, "checkpointed cache at %d; resume with -resume\n", cache.GetLatestHeight())
			return
		default:
		}
		size := min(32, *end-next+1)
		blocks := make([]*walletrpc.CompactBlock, size)
		errors := make([]error, size)
		var group sync.WaitGroup
		slots := make(chan struct{}, *workers)
		for index := range size {
			slots <- struct{}{}
			group.Add(1)
			go func(index, height int) {
				defer group.Done()
				defer func() { <-slots }()
				blocks[index], errors[index] = common.GetBlock(context.Background(), nil, height)
			}(index, next+index)
		}
		group.Wait()
		for index, block := range blocks {
			height := next + index
			if errors[index] != nil || block == nil {
				fatalf("block %d: %v", height, errors[index])
			}
			if block.Height != uint64(height) || (height > 0 && !bytes.Equal(block.PrevHash, previous)) {
				fatalf("block %d does not extend the cached chain", height)
			}
			if err := cache.Add(height, block); err != nil {
				fatalf("cache block %d: %v", height, err)
			}
			previous = block.Hash
		}
		next += size
		if (next-first)%10016 == 0 || next > *end {
			cache.Sync()
			fmt.Fprintf(os.Stderr, "cached %d/%d; %.1f blocks/s\n", next-1, *end, float64(next-first)/time.Since(started).Seconds())
		}
	}
	if err := checkTip(); err != nil {
		fatalf("%v", err)
	}
	if *end == *tip && !bytes.Equal(hash32.ReverseSlice(previous), expected) {
		fatalf("final cache hash mismatch")
	}
	cache.Sync()
	manifest := map[string]interface{}{"chain": "main", "node_tip": *tip, "node_tip_hash": *tipHash,
		"cache_start": 0, "cache_end": cache.GetLatestHeight(), "import_first": first, "seconds": time.Since(started).Seconds(),
		"workers": *workers, "preparation_only": true}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		fatalf("%v", err)
	}
	if err := os.WriteFile(filepath.Join(*dataDir, "import.json"), append(body, '\n'), 0600); err != nil {
		fatalf("%v", err)
	}
	fmt.Println(string(body))
}
