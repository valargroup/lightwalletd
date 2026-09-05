package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/zcash/lightwalletd/hash32"
	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/protobuf/proto"
)

// verifyCacheFiles opens cache files read only; corruption never triggers repair.
// It checks every record and hashes both complete files for benchmark provenance.
func verifyCacheFiles(directory string, height int, tipHash []byte) (map[string]interface{}, error) {
	lengths, err := os.Open(filepath.Join(directory, "lengths"))
	if err != nil {
		return nil, err
	}
	defer lengths.Close()
	blocks, err := os.Open(filepath.Join(directory, "blocks"))
	if err != nil {
		return nil, err
	}
	defer blocks.Close()
	li, err := lengths.Stat()
	if err != nil {
		return nil, err
	}
	bi, err := blocks.Stat()
	if err != nil {
		return nil, err
	}
	if height < 0 || len(tipHash) != 32 || li.Size() != int64(height+1)*4 {
		return nil, fmt.Errorf("unexpected cache coverage")
	}
	ls, bs := sha256.New(), sha256.New()
	lr := bufio.NewReaderSize(io.TeeReader(lengths, ls), 1<<20)
	br := bufio.NewReaderSize(io.TeeReader(blocks, bs), 1<<20)
	previous := make([]byte, 32)
	sizes := &walletrpc.ChainMetadata{}
	var length [4]byte
	var checksum, heightBytes [8]byte
	var body []byte
	var total int64
	for h := 0; h <= height; h++ {
		if _, err := io.ReadFull(lr, length[:]); err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint32(length[:])
		if n < 74 || n > 4_000_000 {
			return nil, fmt.Errorf("invalid cache record length at %d", h)
		}
		if cap(body) < int(n) {
			body = make([]byte, n)
		} else {
			body = body[:n]
		}
		if _, err := io.ReadFull(br, checksum[:]); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(br, body); err != nil {
			return nil, err
		}
		binary.LittleEndian.PutUint64(heightBytes[:], uint64(h))
		sum := fnv.New64a()
		sum.Write(heightBytes[:])
		sum.Write(body)
		if !bytes.Equal(checksum[:], sum.Sum(nil)) {
			return nil, fmt.Errorf("checksum mismatch at %d", h)
		}
		block := new(walletrpc.CompactBlock)
		if err := proto.Unmarshal(body, block); err != nil {
			return nil, err
		}
		if block.Height != uint64(h) || len(block.Hash) != 32 || !bytes.Equal(block.PrevHash, previous) {
			return nil, fmt.Errorf("chain linkage mismatch at %d", h)
		}
		added := &walletrpc.ChainMetadata{}
		for _, tx := range block.Vtx {
			// Record size bounds keep these per-block counts below uint32 capacity.
			added.SaplingCommitmentTreeSize += uint32(len(tx.Outputs))
			added.OrchardCommitmentTreeSize += uint32(len(tx.Actions))
			added.IronwoodCommitmentTreeSize += uint32(len(tx.IronwoodActions))
		}
		sizes, err = addTreeSizes(sizes, added)
		if err != nil {
			return nil, err
		}
		if !proto.Equal(sizes, block.ChainMetadata) {
			return nil, fmt.Errorf("tree size mismatch at %d", h)
		}
		previous = block.Hash
		total += int64(n) + 8
	}
	if _, err := br.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing cache block data: %v", err)
	}
	if total != bi.Size() || !bytes.Equal(hash32.ReverseSlice(previous), tipHash) {
		return nil, fmt.Errorf("cache length or final hash mismatch")
	}
	afterL, err := lengths.Stat()
	if err != nil {
		return nil, err
	}
	afterB, err := blocks.Stat()
	if err != nil {
		return nil, err
	}
	if afterL.Size() != li.Size() || afterB.Size() != bi.Size() || !afterL.ModTime().Equal(li.ModTime()) || !afterB.ModTime().Equal(bi.ModTime()) {
		return nil, fmt.Errorf("cache changed during verification")
	}
	return map[string]interface{}{"height": height, "tip_hash": hex.EncodeToString(tipHash), "records": height + 1, "lengths_bytes": li.Size(), "blocks_bytes": bi.Size(), "lengths_mtime_ns": li.ModTime().UnixNano(), "blocks_mtime_ns": bi.ModTime().UnixNano(), "lengths_sha256": hex.EncodeToString(ls.Sum(nil)), "blocks_sha256": hex.EncodeToString(bs.Sum(nil)), "all_record_checksums_valid": true, "all_height_links_valid": true, "all_tree_sizes_valid": true}, nil
}

func verifyCache(args []string) {
	flags := flag.NewFlagSet("verify-cache", flag.ExitOnError)
	directory := flags.String("data-dir", "", "closed mainnet cache directory")
	height := flags.Int("height", -1, "expected completed height")
	hash := flags.String("tip-hash", "", "expected display-order tip hash")
	output := flags.String("output", "", "new verification manifest")
	flags.Parse(args)
	tipHash, err := hex.DecodeString(*hash)
	if err != nil || len(tipHash) != 32 || *directory == "" || *output == "" || *height < 0 {
		fatalf("invalid verification arguments")
	}
	if _, err := os.Stat(filepath.Join(*directory, "import.lock")); !os.IsNotExist(err) {
		fatalf("cache writer lock exists or cannot be inspected")
	}
	var imported struct {
		CacheEnd    int    `json:"cache_end"`
		NodeTip     int    `json:"node_tip"`
		NodeTipHash string `json:"node_tip_hash"`
	}
	body, err := os.ReadFile(filepath.Join(*directory, "import.json"))
	if err != nil {
		fatalf("cache not complete: %v", err)
	}
	if err = json.Unmarshal(body, &imported); err != nil || imported.CacheEnd != *height || imported.NodeTip != *height || imported.NodeTipHash != *hash {
		fatalf("import manifest does not match target")
	}
	started := time.Now()
	result, err := verifyCacheFiles(filepath.Join(*directory, "db", "main"), *height, tipHash)
	if err != nil {
		fatalf("cache verification: %v", err)
	}
	if _, err := os.Stat(filepath.Join(*directory, "import.lock")); !os.IsNotExist(err) {
		fatalf("writer lock appeared during verification")
	}
	result["seconds"] = time.Since(started).Seconds()
	result["verified_unix"] = time.Now().Unix()
	result["status"] = "complete"
	body, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatalf("verification JSON: %v", err)
	}
	f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		fatalf("verification output: %v", err)
	}
	if _, err = f.Write(append(body, '\n')); err != nil {
		f.Close()
		fatalf("verification output: %v", err)
	}
	if err = f.Close(); err != nil {
		fatalf("verification output: %v", err)
	}
	fmt.Println(string(body))
}
