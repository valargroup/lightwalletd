package main

import (
	"bytes"
	"encoding/binary"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"

	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/protobuf/proto"
)

func writeVerificationFixture(t *testing.T, directory string, badLink, badTrees bool) {
	t.Helper()
	var lengths, blocks bytes.Buffer
	previous := make([]byte, 32)
	for h := 0; h < 3; h++ {
		hash := bytes.Repeat([]byte{byte(h + 1)}, 32)
		block := &walletrpc.CompactBlock{Height: uint64(h), Time: 1, Hash: hash, PrevHash: previous, ChainMetadata: &walletrpc.ChainMetadata{SaplingCommitmentTreeSize: uint32(h + 1)}, Vtx: []*walletrpc.CompactTx{{Outputs: []*walletrpc.CompactSaplingOutput{{}}}}}
		if h == 2 && badLink {
			block.PrevHash = make([]byte, 32)
		}
		if h == 2 && badTrees {
			block.ChainMetadata.SaplingCommitmentTreeSize++
		}
		body, err := proto.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		var height [8]byte
		binary.LittleEndian.PutUint64(height[:], uint64(h))
		sum := fnv.New64a()
		sum.Write(height[:])
		sum.Write(body)
		blocks.Write(sum.Sum(nil))
		blocks.Write(body)
		if err = binary.Write(&lengths, binary.LittleEndian, uint32(len(body))); err != nil {
			t.Fatal(err)
		}
		previous = hash
	}
	for name, data := range map[string][]byte{"lengths": lengths.Bytes(), "blocks": blocks.Bytes()} {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerifyCacheFiles(t *testing.T) {
	for _, mode := range []string{"valid", "checksum", "link", "trees", "trailing", "coverage", "tip"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writeVerificationFixture(t, dir, mode == "link", mode == "trees")
			height := 2
			tip := bytes.Repeat([]byte{3}, 32)
			if mode == "checksum" || mode == "trailing" {
				p := filepath.Join(dir, "blocks")
				data, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "checksum" {
					data[len(data)-1] ^= 1
				} else {
					data = append(data, 0)
				}
				if err = os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "coverage" {
				height++
			}
			if mode == "tip" {
				tip[0]++
			}
			before, err := os.ReadFile(filepath.Join(dir, "blocks"))
			if err != nil {
				t.Fatal(err)
			}
			result, err := verifyCacheFiles(dir, height, tip)
			if (mode == "valid") != (err == nil) {
				t.Fatalf("result=%v err=%v", result, err)
			}
			after, readErr := os.ReadFile(filepath.Join(dir, "blocks"))
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("verifier changed cache")
			}
		})
	}
}
