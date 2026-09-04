// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

func blockHash(height int) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("lightwalletd-load-lab-block-%d", height)))
}

func displayBlockHash(height int) string {
	hash := blockHash(height)
	for left, right := 0, len(hash)-1; left < right; left, right = left+1, right-1 {
		hash[left], hash[right] = hash[right], hash[left]
	}
	return hex.EncodeToString(hash[:])
}

func mempoolTxID(index int) string {
	var txid [32]byte
	binary.BigEndian.PutUint64(txid[24:], uint64(index+1))
	return hex.EncodeToString(txid[:])
}

func protocolTxID(index int) []byte {
	bigEndian, err := hex.DecodeString(mempoolTxID(index))
	if err != nil {
		panic(err)
	}
	for left, right := 0, len(bigEndian)-1; left < right; left, right = left+1, right-1 {
		bigEndian[left], bigEndian[right] = bigEndian[right], bigEndian[left]
	}
	return bigEndian
}

func repeatedBytes(length int, seed byte) []byte {
	result := make([]byte, length)
	for i := range result {
		result[i] = seed + byte(i*17)
	}
	return result
}
