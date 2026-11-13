// Command ledgerd serves the ledger API.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"example.com/ledger/api"
	"example.com/ledger/ledger"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	seedPath := flag.String("seed", "", "JSON file of opening accounts")
	flag.Parse()

	seed, err := readSeed(*seedPath)
	if err != nil {
		log.Fatal(err)
	}
	store, err := ledger.NewStore(ledger.SystemClock(), seed)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           api.New(store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
	log.Printf("ledgerd listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}

// readSeed reads opening accounts from a JSON array, or returns none when
// path is empty.
func readSeed(path string) ([]ledger.OpeningAccount, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var seed []ledger.OpeningAccount
	if err := dec.Decode(&seed); err != nil {
		return nil, fmt.Errorf("seed %s: %w", path, err)
	}
	return seed, nil
}
