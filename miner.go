package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jrick/wsrpc/v2"
)

func init() {
	log.SetFlags(0)
}

var (
	targetBlockTime = flag.Duration("blocktime", 2*time.Minute, "target block duration")
	retryDuration   = flag.Duration("retry", 30*time.Second, "duration to wait before retries after errors")
	ws              = flag.String("ws", "wss://localhost:19109/ws", "websocket endpoint")
	ca              = flag.String("ca", "", "path to dcrd certificate authority")
	clientCert      = flag.String("cert", "", "path to client certificate")
	clientKey       = flag.String("key", "", "path to client certificate key")
)

func pollBestBlockTime(ctx context.Context, c *wsrpc.Client) (t time.Time, err error) {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	var bestBlockHash string
	err = c.Call(ctx, "getbestblockhash", &bestBlockHash)
	if err != nil {
		err = fmt.Errorf("getbestblockhash: %w", err)
		return
	}

	var blockHeader struct {
		Time int64 `json:"time"`
	}
	err = c.Call(ctx, "getblockheader", &blockHeader, bestBlockHash)
	if err != nil {
		err = fmt.Errorf("getblockheader: %w", err)
		return
	}

	return time.Unix(blockHeader.Time, 0), nil
}

func generate(ctx context.Context, c *wsrpc.Client) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var hashes []string
	err := c.Call(ctx, "generate", &hashes, 1)
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}
	return hashes[0], nil
}

func setupTLS() *tls.Config {
	caPool := x509.NewCertPool()
	caCerts, err := os.ReadFile(*ca)
	if err != nil {
		log.Fatal(err)
	}
	if !caPool.AppendCertsFromPEM(caCerts) {
		log.Fatal("no certificates found in CA file")
	}

	keypair, err := tls.LoadX509KeyPair(*clientCert, *clientKey)
	if err != nil {
		log.Fatal(err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{keypair},
		RootCAs:      caPool,
	}
}

func main() {
	flag.Parse()

	ctx := context.Background()

	tc := setupTLS()
	c, err := wsrpc.Dial(ctx, *ws, wsrpc.WithTLSConfig(tc))
	if err != nil {
		log.Fatal(err)
	}

	for {
		tipTime, err := pollBestBlockTime(ctx, c)
		if err != nil {
			log.Printf("warn: pollBestBlockTime returned error: %v", err)
			time.Sleep(*retryDuration)
			continue
		}

		mineTime := tipTime.Add(*targetBlockTime)
		log.Printf("best block time: %v; mining scheduled time: %v", tipTime, mineTime)

		if wait := time.Until(mineTime); wait > 0 {
			log.Printf("sleeping %v", wait.Truncate(time.Millisecond))
			time.Sleep(wait)
			continue
		}

		log.Print("starting cpu miner")
		hash, err := generate(ctx, c)
		if err != nil {
			// Even if this errors, chances are we successfully
			// mined a block (or soon will).  There is no request
			// cancellation with websockets (other than
			// disconnecting), so a timed-out on the client side
			// could still be running the miner on the server.
			// Log the error just in case it holds something more
			// interesting than a timeout.
			log.Printf("generate: %v", err)

			minSleep := min(*retryDuration, *targetBlockTime)
			time.Sleep(minSleep)
		} else {
			log.Printf("generate: mined block %s", hash)
		}

		wait := time.Now().Add(*targetBlockTime)
		log.Printf("polling blocks again at %v", wait.Truncate(100*time.Millisecond))
		time.Sleep(time.Until(wait))
	}
}
