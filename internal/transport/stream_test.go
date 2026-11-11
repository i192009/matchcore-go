package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	pb "github.com/matchcore/matchcore-go/api/gen/matchcore/v1"
	"github.com/matchcore/matchcore-go/internal/core"
	"github.com/matchcore/matchcore-go/internal/store"
	"google.golang.org/grpc"
)

type fakeReader struct{ pb.ExchangeClient }

func (fakeReader) GetOrderBook(ctx context.Context, q *pb.Query, opts ...grpc.CallOption) (*pb.Result, error) {
	data, _ := json.Marshal(core.Depth{Instrument: q.Instrument, Sequence: 0, Bids: []core.Level{}, Asks: []core.Level{}})
	return &pb.Result{Json: data}, nil
}
func testHub(t *testing.T) *Hub {
	t.Helper()
	t.Setenv("API_KEYS", `[{"key":"test-key-for-alice-123","account":"alice","role":"trader"}]`)
	auth, e := LoadAuth()
	if e != nil {
		t.Fatal(e)
	}
	return NewHub(fakeReader{}, auth, []string{"http://localhost:5173"})
}
func TestPublicStreamPrivacyAndBoundedSlowClient(t *testing.T) {
	h := testHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &streamClient{identity: Principal{Account: "alice"}, subs: map[string]*subscription{"BTC-USD": {}}, queue: make(chan any, 1), cancel: cancel}
	h.clients[c] = true
	env := store.Envelope{Instrument: "BTC-USD", Sequence: 1, Plan: core.Plan{Changes: []core.Change{{After: core.Order{ID: "secret-order", Account: "bob", Side: core.Sell, Price: 100, Remaining: 5, Status: core.New}}}, Trades: []core.Trade{{ID: "trade", Buyer: "alice", Seller: "bob", Price: 100, Quantity: 1}}}}
	h.Broadcast(env)
	value := <-c.queue
	data, _ := json.Marshal(value)
	if strings.Contains(string(data), "bob") || strings.Contains(string(data), "secret-order") {
		t.Fatal("private data leaked", string(data))
	}
	h.Broadcast(env)
	h.Broadcast(env)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("slow client not disconnected")
	}
}
func TestWebSocketOriginAuthAndPrivateOwnership(t *testing.T) {
	h := testHub(t)
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	if c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://evil.example"}}}); err == nil {
		_ = c.CloseNow()
		t.Fatal("cross-origin upgrade allowed")
	}
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{"http://localhost:5173"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err = wsjson.Write(ctx, c, map[string]string{"op": "auth", "token": "test-key-for-alice-123"}); err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err = wsjson.Read(ctx, c, &message); err != nil || message["type"] != "authenticated" {
		t.Fatal(message, err)
	}
	if err = wsjson.Write(ctx, c, map[string]string{"op": "subscribe", "instrument": "BTC-USD", "account": "bob"}); err != nil {
		t.Fatal(err)
	}
	if err = wsjson.Read(ctx, c, &message); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatal("private subscription not denied", err)
	}
}
func TestConcurrentSubscriberDisconnects(t *testing.T) {
	h := testHub(t)
	var wg sync.WaitGroup
	for n := 0; n < 100; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithCancel(context.Background())
			c := &streamClient{identity: Principal{Account: "alice"}, subs: map[string]*subscription{"BTC-USD": {}}, queue: make(chan any, 1), cancel: cancel}
			h.mu.Lock()
			h.clients[c] = true
			h.mu.Unlock()
			h.Broadcast(store.Envelope{Instrument: "BTC-USD", Sequence: 1})
			cancel()
			<-ctx.Done()
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
		}()
	}
	wg.Wait()
	if len(h.clients) != 0 {
		t.Fatal("subscriber leak")
	}
}
