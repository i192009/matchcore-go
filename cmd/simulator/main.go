// Simulator submits a deterministic, bounded sequence through the real HTTP API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	base := flag.String("url", "http://localhost:8080", "gateway URL")
	run := flag.String("run", "demo-1", "idempotency namespace; use a new run for new activity")
	count := flag.Int("count", 120, "number of iterations")
	delay := flag.Duration("delay", 200*time.Millisecond, "delay between iterations")
	flag.Parse()
	buy, sell := os.Getenv("SIM_BUY_KEY"), os.Getenv("SIM_SELL_KEY")
	if buy == "" || sell == "" {
		fmt.Fprintln(os.Stderr, "SIM_BUY_KEY and SIM_SELL_KEY required (different funded accounts)")
		os.Exit(1)
	}
	client := http.Client{Timeout: 10 * time.Second}
	if *count < 0 || *delay < 0 {
		fmt.Fprintln(os.Stderr, "count and delay must be nonnegative")
		os.Exit(1)
	}
	for i, instrument := range []string{"BTC-USD", "ETH-USD", "SOL-USD"} {
		mid := []int64{5000, 300, 15}[i]
		for level := int64(1); level <= 8; level++ {
			for leg := 0; leg < 2; leg++ {
				key, side, price := sell, "SELL", mid+level+5
				if leg == 1 {
					key, side, price = buy, "BUY", mid-level-5
				}
				body := map[string]any{"client_id": fmt.Sprintf("%s-ladder-%s-%d-%d", *run, instrument, level, leg), "instrument": instrument, "side": side, "type": "LIMIT", "tif": "GTC", "price": price, "quantity": 20 + level*10}
				if err := send(&client, *base, key, body); err != nil {
					fmt.Fprintln(os.Stderr, err)
					os.Exit(1)
				}
			}
		}
	}
	for n := 0; n < *count; n++ {
		instrument := []string{"BTC-USD", "ETH-USD", "SOL-USD"}[n%3]
		mid := []int64{5000, 300, 15}[n%3] + int64((n/3)%9) - 4
		for leg := 0; leg < 2; leg++ {
			key, side := sell, "SELL"
			if leg == 1 {
				key, side = buy, "BUY"
			}
			body := map[string]any{"client_id": fmt.Sprintf("%s-%d-%d", *run, n, leg), "instrument": instrument, "side": side, "type": "LIMIT", "tif": "GTC", "price": mid, "quantity": int64(20)}
			if err := send(&client, *base, key, body); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
		fmt.Printf("%s iteration=%d instrument=%s price_ticks=%d\n", *run, n, instrument, mid)
		time.Sleep(*delay)
	}
}

func send(client *http.Client, base, key string, body map[string]any) error {
	data, e := json.Marshal(body)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(context.Background(), "POST", base+"/api/orders", bytes.NewReader(data))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	response, e := io.ReadAll(io.LimitReader(res.Body, 65536))
	if e != nil {
		return e
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("order failed: %d %s", res.StatusCode, response)
	}
	return nil
}
