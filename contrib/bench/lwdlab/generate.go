// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sirupsen/logrus"
	"github.com/zcash/lightwalletd/common"
	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/protobuf/proto"
)

func generateCache(args []string) {
	flags := flag.NewFlagSet("generate-cache", flag.ExitOnError)
	dataDir := flags.String("data-dir", "", "new lightwalletd data directory")
	blockCount := flags.Int("blocks", 2048, "number of compact blocks")
	txPerBlock := flags.Int("tx-per-block", 64, "transactions in each compact block")
	shape := flags.String("shape", "mixed", "mixed, segregated, or shielded transaction pools")
	chain := flags.String("chain", "regtest", "cache chain name")
	_ = flags.Parse(args)
	if *dataDir == "" || *blockCount < 1 || *txPerBlock < 0 {
		flags.Usage()
		os.Exit(2)
	}
	if _, err := os.Stat(*dataDir); !os.IsNotExist(err) {
		fatalf("data directory %q already exists", *dataDir)
	}

	logger := logrus.New()
	logger.SetOutput(io.Discard)
	common.Log = logger.WithField("component", "lwdlab")

	txs := compactTransactions(*txPerBlock)
	for i, tx := range txs {
		switch *shape {
		case "mixed":
		case "segregated":
			// Deliberately illustrative, not a measured mainnet distribution.
			if i%20 < 17 {
				tx.Spends, tx.Outputs, tx.Actions, tx.IronwoodActions = nil, nil, nil, nil
			} else {
				tx.Vin, tx.Vout, tx.IronwoodActions = nil, nil, nil
				if i%20 == 17 {
					tx.Actions = nil
				} else {
					tx.Spends, tx.Outputs = nil, nil
				}
			}
		case "shielded":
			tx.Vin, tx.Vout, tx.IronwoodActions = nil, nil, nil
			tx.Spends, tx.Outputs = nil, nil
		default:
			fatalf("unknown shape %q", *shape)
		}
	}
	sample := &walletrpc.CompactBlock{Height: 1, Hash: repeatedBytes(32, 1), PrevHash: repeatedBytes(32, 2), Vtx: txs}
	sampleBytes := proto.Size(sample)
	cache := common.NewBlockCache(filepath.Join(*dataDir, "db"), *chain, 0, 0)
	for height := range *blockCount {
		hash := blockHash(height)
		var previous []byte
		if height > 0 {
			prevHash := blockHash(height - 1)
			previous = prevHash[:]
		}
		block := &walletrpc.CompactBlock{
			Height:   uint64(height),
			Hash:     hash[:],
			PrevHash: previous,
			Time:     uint32(1_700_000_000 + height),
			Vtx:      txs,
			ChainMetadata: &walletrpc.ChainMetadata{
				SaplingCommitmentTreeSize:  uint32(height * *txPerBlock),
				OrchardCommitmentTreeSize:  uint32(height * *txPerBlock),
				IronwoodCommitmentTreeSize: uint32(height * *txPerBlock),
			},
		}
		if err := cache.Add(height, block); err != nil {
			fatalf("add block %d: %v", height, err)
		}
	}
	cache.Sync()
	cache.Close()
	result := map[string]any{
		"data_dir":           *dataDir,
		"chain":              *chain,
		"shape":              *shape,
		"blocks":             *blockCount,
		"tx_per_block":       *txPerBlock,
		"sample_block_bytes": sampleBytes,
		"tip_height":         *blockCount - 1,
		"tip_hash":           displayBlockHash(*blockCount - 1),
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatalf("encode result: %v", err)
	}
}

func compactTransactions(count int) []*walletrpc.CompactTx {
	txs := make([]*walletrpc.CompactTx, count)
	for i := range txs {
		seed := byte(i*31 + 1)
		txs[i] = &walletrpc.CompactTx{
			Index: uint64(i),
			Txid:  repeatedBytes(32, seed),
			Fee:   uint32(1000 + i),
			Spends: []*walletrpc.CompactSaplingSpend{{
				Nf: repeatedBytes(32, seed+1),
			}},
			Outputs: []*walletrpc.CompactSaplingOutput{{
				Cmu:          repeatedBytes(32, seed+2),
				EphemeralKey: repeatedBytes(32, seed+3),
				Ciphertext:   repeatedBytes(52, seed+4),
			}},
			Actions: []*walletrpc.CompactOrchardAction{{
				Nullifier:    repeatedBytes(32, seed+5),
				Cmx:          repeatedBytes(32, seed+6),
				EphemeralKey: repeatedBytes(32, seed+7),
				Ciphertext:   repeatedBytes(52, seed+8),
			}},
			IronwoodActions: []*walletrpc.CompactOrchardAction{{
				Nullifier:    repeatedBytes(32, seed+9),
				Cmx:          repeatedBytes(32, seed+10),
				EphemeralKey: repeatedBytes(32, seed+11),
				Ciphertext:   repeatedBytes(52, seed+12),
			}},
			Vin: []*walletrpc.CompactTxIn{{
				PrevoutTxid:  repeatedBytes(32, seed+13),
				PrevoutIndex: uint32(i),
			}},
			Vout: []*walletrpc.TxOut{{
				Value:        uint64(50_000 + i),
				ScriptPubKey: repeatedBytes(25, seed+14),
			}},
		}
	}
	return txs
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
