// Copyright (c) 2026 The Zcash developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or https://www.opensource.org/licenses/mit-license.php .

// Command lwdlab provides deterministic cache generation, a JSON-RPC backend,
// and a concurrent gRPC load driver for lightwalletd performance comparisons.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "generate-cache":
		generateCache(os.Args[2:])
	case "backend":
		runBackend(os.Args[2:])
	case "load":
		runLoad(os.Args[2:])
	case "record":
		runRecord(os.Args[2:])
	case "import-cache":
		importCache(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lwdlab <generate-cache|backend|load|record|import-cache> [flags]")
	os.Exit(2)
}
