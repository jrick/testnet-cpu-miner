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
	ctx, cancel := context.WithTimeout(ctx, 16*time.Second)
	defer cancel()

	log.Print("starting cpu miner")

	var hashes []string
	call := c.Go(ctx, "generate", &hashes, nil, 1)

	t := time.NewTimer(15 * time.Second)
	defer t.Stop()
	var stopped bool
	select {
	case <-call.Done():
	case <-t.C:
		log.Print("stopping cpu miner")

		ctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		err := c.Call(ctx, "generate", nil, 0)
		cancel()
		if err != nil {
			log.Printf("failed to disable CPU miner: %v", err)
			break
		}
		stopped = true

		<-call.Done()
	}

	_, err := call.Result()
	if err != nil {
		// Log unexpected errors not caused by stopping the miner.
		if !stopped {
			log.Printf("generate: %v", err)
		}
		return "", err
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

	// Hacky, but should probably be long enough to bring up RPC.
	// Exact timing of when we get to work is not important.
	log.Printf("connecting RPC in %v", *targetBlockTime)
	time.Sleep(*targetBlockTime)

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

		var sleep time.Duration

		hash, err := generate(ctx, c)
		if err != nil {
			sleep = min(*retryDuration, *targetBlockTime)
		} else {
			log.Printf("generate: mined block %s", hash)

			sleep = *targetBlockTime
		}

		nextPoll := time.Now().Add(sleep).Truncate(100 * time.Millisecond)
		log.Printf("polling blocks again at %v", nextPoll)
		time.Sleep(sleep)
	}
}
