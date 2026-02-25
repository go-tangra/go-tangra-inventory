package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"go-tangra-inventory/internal/collector"
)

func main() {
	outputFile := flag.String("o", "", "write JSON output to file instead of stdout")
	flag.Parse()

	inv, err := collector.Collect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	var w *os.File
	if *outputFile != "" {
		f, err := os.Create(*outputFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot create output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	} else {
		w = os.Stdout
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(inv); err != nil {
		fmt.Fprintf(os.Stderr, "error: encoding inventory: %v\n", err)
		os.Exit(1)
	}

	if *outputFile != "" {
		fmt.Fprintf(os.Stderr, "inventory written to %s\n", *outputFile)
	}
}
