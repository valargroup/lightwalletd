package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcash/lightwalletd/walletrpc"
	"google.golang.org/protobuf/proto"
)

func TestAddTreeSizes(t *testing.T) {
	previous := &walletrpc.ChainMetadata{SaplingCommitmentTreeSize: 100, OrchardCommitmentTreeSize: 200, IronwoodCommitmentTreeSize: 300}
	added := &walletrpc.ChainMetadata{SaplingCommitmentTreeSize: 2, IronwoodCommitmentTreeSize: 5}
	result, err := addTreeSizes(previous, added)
	want := &walletrpc.ChainMetadata{SaplingCommitmentTreeSize: 102, OrchardCommitmentTreeSize: 200, IronwoodCommitmentTreeSize: 305}
	if err != nil || !proto.Equal(result, want) {
		t.Fatalf("got %v, %v", result, err)
	}
	if previous.SaplingCommitmentTreeSize != 100 || added.SaplingCommitmentTreeSize != 2 {
		t.Fatal("modified earlier block metadata")
	}
	for _, pool := range []string{"sapling", "orchard", "ironwood"} {
		t.Run(pool, func(t *testing.T) {
			a, b := &walletrpc.ChainMetadata{}, &walletrpc.ChainMetadata{}
			switch pool {
			case "sapling":
				a.SaplingCommitmentTreeSize = math.MaxUint32
				b.SaplingCommitmentTreeSize = 1
			case "orchard":
				a.OrchardCommitmentTreeSize = math.MaxUint32
				b.OrchardCommitmentTreeSize = 1
			case "ironwood":
				a.IronwoodCommitmentTreeSize = math.MaxUint32
				b.IronwoodCommitmentTreeSize = 1
			}
			if _, err := addTreeSizes(a, b); err == nil {
				t.Fatal("accepted wrapped tree size")
			}
		})
	}
	if _, err := addTreeSizes(nil, added); err == nil {
		t.Fatal("accepted missing previous tree state")
	}
	if _, err := addTreeSizes(previous, nil); err == nil {
		t.Fatal("accepted missing commitment counts")
	}
}

func TestSummaryHelperRequiresMatchingPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(path, []byte("not executable"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, pin := range []string{"", strings.Repeat("0", 64)} {
		if p, err := startSummaryProcess(path, pin); err == nil || p != nil {
			t.Fatal("accepted unverified helper")
		}
	}
}
