package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/zcash/lightwalletd/common"
	"github.com/zcash/lightwalletd/hash32"
	"github.com/zcash/lightwalletd/parser"
	"github.com/zcash/lightwalletd/walletrpc"
)

type blockSummary struct {
	Height           uint64
	Hash             string
	Tx               []string
	CommitmentsAdded map[string]uint64 `json:"commitments_added"`
}

// summaryProcess is a preparation-only adapter to the pinned transaction library.
// A mutex keeps each request paired with its response while raw RPC fetches overlap.
type summaryProcess struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	input   io.WriteCloser
	encoder *json.Encoder
	decoder *json.Decoder
}

func startSummaryProcess(path, expectedSHA string) (*summaryProcess, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	expected, err := hex.DecodeString(expectedSHA)
	if err != nil || len(expected) != 32 {
		return nil, fmt.Errorf("summary helper requires a SHA-256 pin")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(actual[:], expected) {
		return nil, fmt.Errorf("summary helper checksum mismatch")
	}
	cmd := exec.Command(path)
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	return &summaryProcess{cmd: cmd, input: input, encoder: json.NewEncoder(input), decoder: json.NewDecoder(output)}, nil
}

func (p *summaryProcess) close() {
	p.input.Close()
	// No import is valid after a helper failure. Kill also bounds cleanup if it hangs.
	p.cmd.Process.Kill()
	p.cmd.Wait()
}

func (p *summaryProcess) getBlock(ctx context.Context, height int) (*walletrpc.CompactBlock, error) {
	if height <= 0 {
		return nil, fmt.Errorf("raw preparation must resume after genesis")
	}
	heightJSON, _ := json.Marshal(strconv.Itoa(height))
	result, err := common.RawRequest(ctx, "getblock", []json.RawMessage{heightJSON, json.RawMessage("0")})
	if err != nil {
		return nil, err
	}
	var rawHex string
	if err = json.Unmarshal(result, &rawHex); err != nil {
		return nil, err
	}
	var summary blockSummary
	p.mu.Lock()
	deadline := time.AfterFunc(120*time.Second, func() { p.cmd.Process.Kill() })
	err = p.encoder.Encode(map[string]interface{}{"height": height, "hex": rawHex})
	if err == nil {
		err = p.decoder.Decode(&summary)
	}
	deadline.Stop()
	p.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("block summary: %w", err)
	}
	raw, err := hex.DecodeString(rawHex)
	if err != nil {
		return nil, err
	}
	block := parser.NewBlock()
	rest, err := block.ParseFromSlice(raw)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 || block.GetHeight() != height || summary.Height != uint64(height) || block.GetDisplayHashString() != summary.Hash || len(block.Transactions()) != len(summary.Tx) {
		return nil, fmt.Errorf("raw block and summary identity differ at %d", height)
	}
	counts := map[string]uint64{"sapling": 0, "orchard": 0, "ironwood": 0}
	for i, tx := range block.Transactions() {
		id, err := hash32.Decode(summary.Tx[i])
		if err != nil {
			return nil, err
		}
		tx.SetTxID(hash32.Reverse(id))
		counts["sapling"] += uint64(tx.SaplingOutputsCount())
		counts["orchard"] += uint64(tx.OrchardActionsCount())
		counts["ironwood"] += uint64(tx.IronwoodActionsCount())
	}
	for pool, count := range counts {
		actual, ok := summary.CommitmentsAdded[pool]
		if !ok || actual != count || count > math.MaxUint32 {
			return nil, fmt.Errorf("parsers disagree on %s commitment count at %d", pool, height)
		}
	}
	compact := block.ToCompact()
	// These are increments until the ordered writer adds the preceding tree sizes.
	compact.ChainMetadata = &walletrpc.ChainMetadata{SaplingCommitmentTreeSize: uint32(counts["sapling"]), OrchardCommitmentTreeSize: uint32(counts["orchard"]), IronwoodCommitmentTreeSize: uint32(counts["ironwood"])}
	return compact, nil
}

// addTreeSizes converts per-block commitment counts to cumulative tree sizes.
// It returns a new value so cached metadata and earlier blocks remain immutable.
func addTreeSizes(previous, added *walletrpc.ChainMetadata) (*walletrpc.ChainMetadata, error) {
	if previous == nil || added == nil {
		return nil, fmt.Errorf("missing commitment tree sizes")
	}
	sum := func(a, b uint32) (uint32, error) {
		if uint64(a)+uint64(b) > math.MaxUint32 {
			return 0, fmt.Errorf("commitment tree size exceeds compact protocol capacity")
		}
		return a + b, nil
	}
	s, err := sum(previous.SaplingCommitmentTreeSize, added.SaplingCommitmentTreeSize)
	if err != nil {
		return nil, err
	}
	o, err := sum(previous.OrchardCommitmentTreeSize, added.OrchardCommitmentTreeSize)
	if err != nil {
		return nil, err
	}
	i, err := sum(previous.IronwoodCommitmentTreeSize, added.IronwoodCommitmentTreeSize)
	if err != nil {
		return nil, err
	}
	return &walletrpc.ChainMetadata{SaplingCommitmentTreeSize: s, OrchardCommitmentTreeSize: o, IronwoodCommitmentTreeSize: i}, nil
}
