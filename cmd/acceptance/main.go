// Acceptance validates the actual Compose services before and after a restart.
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
	"reflect"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/matchcore/matchcore-go/internal/core"
	"github.com/matchcore/matchcore-go/internal/store"
	"github.com/matchcore/matchcore-go/internal/transport"
)

type state struct {
	Sell       store.Envelope
	Buy        store.Envelope
	BuyRequest map[string]any
	Alice      []store.Balance
	Bob        []store.Balance
	Fees       []store.Balance
	Book       core.Depth
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "ACCEPTANCE FAILED:", e)
		os.Exit(1)
	}
}
func run() error {
	phase := flag.String("phase", "before", "before or after engine restart")
	file := flag.String("state", "/artifacts/acceptance-state.json", "state artifact")
	base := flag.String("url", "http://gateway:8080", "gateway URL")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var keys []transport.Credential
	if e := json.Unmarshal([]byte(os.Getenv("API_KEYS")), &keys); e != nil {
		return e
	}
	credentials := map[string]string{}
	for _, k := range keys {
		credentials[k.Account] = k.Key
	}
	if credentials["alice"] == "" || credentials["bob"] == "" {
		return fmt.Errorf("alice and bob demo credentials required")
	}
	request := func(account, path string, body any, out any) error {
		method := "GET"
		var data []byte
		if body != nil {
			method = "POST"
			data, _ = json.Marshal(body)
		}
		req, e := http.NewRequestWithContext(ctx, method, *base+"/api/"+path, bytes.NewReader(data))
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+credentials[account])
		req.Header.Set("Content-Type", "application/json")
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		v, e := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		if e != nil {
			return e
		}
		if res.StatusCode != 200 {
			return fmt.Errorf("%s: HTTP %d: %s", path, res.StatusCode, v)
		}
		return json.Unmarshal(v, out)
	}
	db, e := store.Open(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer db.Pool.Close()
	verifyDB := func(st state) error {
		if len(st.Buy.Plan.Trades) != 1 {
			return fmt.Errorf("expected one execution")
		}
		id := st.Buy.Plan.Trades[0].ID
		var trades, settlements, events int
		e := db.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM trades WHERE id=$1),(SELECT count(*) FROM settlements WHERE trade_id=$1),(SELECT count(*) FROM events WHERE id=$2)", id, st.Buy.ID).Scan(&trades, &settlements, &events)
		if e != nil {
			return e
		}
		if trades != 1 || settlements != 1 || events != 1 {
			return fmt.Errorf("durable trade, settlement or event missing/duplicated")
		}
		return nil
	}
	if *phase == "after" {
		data, e := os.ReadFile(*file)
		if e != nil {
			return e
		}
		var st state
		if e = json.Unmarshal(data, &st); e != nil {
			return e
		}
		var book core.Depth
		if e = request("alice", "book?instrument=BTC-USD", nil, &book); e != nil {
			return e
		}
		if !reflect.DeepEqual(book, st.Book) {
			return fmt.Errorf("recovered book differs")
		}
		for _, a := range []string{"alice", "bob"} {
			var b []store.Balance
			if e = request(a, "balances", nil, &b); e != nil {
				return e
			}
			expected := st.Alice
			if a == "bob" {
				expected = st.Bob
			}
			if !reflect.DeepEqual(b, expected) {
				return fmt.Errorf("%s balances changed across restart", a)
			}
		}
		var again store.Envelope
		if e = request("alice", "orders", st.BuyRequest, &again); e != nil {
			return e
		}
		if !reflect.DeepEqual(again, st.Buy) {
			return fmt.Errorf("retry did not return original result")
		}
		var after core.Depth
		if e = request("alice", "book?instrument=BTC-USD", nil, &after); e != nil {
			return e
		}
		if !reflect.DeepEqual(book, after) {
			return fmt.Errorf("retry changed state")
		}
		fees, e := db.Balances(ctx, "fees")
		if e != nil {
			return e
		}
		if !reflect.DeepEqual(fees, st.Fees) {
			return fmt.Errorf("duplicate fee effect")
		}
		if e = verifyDB(st); e != nil {
			return e
		}
		fmt.Println("PASS: restart reconstructed committed state; identical retry created no order, trade, event or financial effects")
		return nil
	}
	if *phase != "before" {
		return fmt.Errorf("unknown phase")
	}
	var st state
	var initialAlice, initialBob []store.Balance
	if e = request("alice", "balances", nil, &initialAlice); e != nil {
		return e
	}
	if e = request("bob", "balances", nil, &initialBob); e != nil {
		return e
	}
	initialFees, e := db.Balances(ctx, "fees")
	if e != nil {
		return e
	}
	var book core.Depth
	if e = request("alice", "book?instrument=BTC-USD", nil, &book); e != nil {
		return e
	}
	if len(book.Bids) > 0 || len(book.Asks) > 0 {
		return fmt.Errorf("acceptance requires an empty BTC-USD book; run before the simulator or cancel demo orders")
	}
	wsURL := "ws" + strings.TrimPrefix(*base, "http") + "/ws"
	conn, _, e := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://localhost:5173"}}})
	if e != nil {
		return e
	}
	defer conn.CloseNow()
	if e = wsjson.Write(ctx, conn, map[string]string{"op": "auth", "token": credentials["alice"]}); e != nil {
		return e
	}
	var msg map[string]any
	if e = wsjson.Read(ctx, conn, &msg); e != nil {
		return e
	}
	if e = wsjson.Write(ctx, conn, map[string]string{"op": "subscribe", "instrument": "BTC-USD"}); e != nil {
		return e
	}
	if e = wsjson.Read(ctx, conn, &msg); e != nil {
		return e
	}
	if msg["type"] != "snapshot" {
		return fmt.Errorf("missing initial stream snapshot")
	}
	run := fmt.Sprint(time.Now().UnixNano())
	sellBody := map[string]any{"client_id": "accept-sell-" + run, "instrument": "BTC-USD", "side": "SELL", "type": "LIMIT", "tif": "GTC", "price": 5000, "quantity": 2000}
	st.BuyRequest = map[string]any{"client_id": "accept-buy-" + run, "instrument": "BTC-USD", "side": "BUY", "type": "LIMIT", "tif": "GTC", "price": 5000, "quantity": 1000}
	if e = request("bob", "orders", sellBody, &st.Sell); e != nil {
		return e
	}
	if e = request("alice", "orders", st.BuyRequest, &st.Buy); e != nil {
		return e
	}
	if len(st.Buy.Plan.Trades) != 1 {
		return fmt.Errorf("expected exactly one trade")
	}
	trade := st.Buy.Plan.Trades[0]
	if trade.Quantity != 1000 || trade.Price != 5000 || trade.Maker != st.Sell.Order.ID || st.Buy.Order.Status != core.Filled {
		return fmt.Errorf("execution price, priority, quantity or buyer state wrong")
	}
	var remaining core.Order
	if e = request("bob", "order?order_id="+st.Sell.Order.ID, nil, &remaining); e != nil {
		return e
	}
	if remaining.Remaining != 1000 || remaining.Reserved != 1000 || remaining.Status != core.Partial {
		return fmt.Errorf("sell remainder incorrect")
	}
	streamed := false
	for !streamed {
		if e = wsjson.Read(ctx, conn, &msg); e != nil {
			return e
		}
		if msg["type"] == "delta" {
			data, _ := json.Marshal(msg["trades"])
			var trades []struct {
				ID string `json:"id"`
			}
			if e = json.Unmarshal(data, &trades); e != nil {
				return e
			}
			for _, t := range trades {
				if t.ID == trade.ID {
					streamed = true
				}
			}
		}
	}
	cancelBody := map[string]any{"key": "accept-cancel-" + run, "instrument": "BTC-USD", "order_id": st.Sell.Order.ID}
	var cancelled store.Envelope
	if e = request("bob", "cancel", cancelBody, &cancelled); e != nil {
		return e
	}
	if e = request("alice", "balances", nil, &st.Alice); e != nil {
		return e
	}
	if e = request("bob", "balances", nil, &st.Bob); e != nil {
		return e
	}
	st.Fees, e = db.Balances(ctx, "fees")
	if e != nil {
		return e
	}
	if e = request("alice", "book?instrument=BTC-USD", nil, &st.Book); e != nil {
		return e
	}
	balance := func(bs []store.Balance, asset string) store.Balance {
		for _, b := range bs {
			if b.Asset == asset {
				return b
			}
		}
		return store.Balance{}
	}
	a0, a1 := balance(initialAlice, "USD"), balance(st.Alice, "USD")
	b0, b1 := balance(initialBob, "USD"), balance(st.Bob, "USD")
	if a0.Total-a1.Total != 5_005_000 || b1.Total-b0.Total != 4_995_000 || balance(st.Fees, "USD").Total-balance(initialFees, "USD").Total != 10_000 {
		return fmt.Errorf("quote movement or fee incorrect")
	}
	if balance(st.Alice, "BTC").Total-balance(initialAlice, "BTC").Total != 1000 || balance(initialBob, "BTC").Total-balance(st.Bob, "BTC").Total != 1000 || balance(st.Bob, "BTC").Reserved != 0 {
		return fmt.Errorf("base movement or reservation release incorrect")
	}
	if e = verifyDB(st); e != nil {
		return e
	}
	data, e := json.MarshalIndent(st, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(*file, data, 0600); e != nil {
		return e
	}
	fmt.Println("PASS: real HTTP/gRPC orders matched 1 BTC at $50,000; partial seller, filled buyer, balanced settlement, fees, committed WebSocket execution, cancellation and reservation release verified")
	return nil
}
