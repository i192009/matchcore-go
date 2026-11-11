package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/matchcore/matchcore-go/internal/core"
	"github.com/matchcore/matchcore-go/internal/transport"
	"github.com/nats-io/nats.go"
)

func TestCrashAfterBrokerAckAndDuplicateDelivery(t *testing.T) {
	s := database(t)
	eng := start(t, s)
	submit(t, eng, command("alice", "broker-crash", "BTC-USD", core.Buy, 10, 1))
	eng.Close()
	nc, js, err := transport.ConnectNATS(os.Getenv("MATCHCORE_TEST_NATS_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	suffix := fmt.Sprint(time.Now().UnixNano())
	calls := 0
	publish := func(id, instrument string, payload []byte) error {
		m := nats.NewMsg("matchcore.events." + instrument)
		m.Data = payload
		m.Header.Set(nats.MsgIdHdr, id+"-"+suffix)
		ack, e := js.PublishMsg(m)
		if e != nil {
			return e
		}
		calls++
		if calls == 1 {
			if ack.Duplicate {
				t.Fatal("first publication was already present")
			}
			return errors.New("process terminated after durable broker ack")
		}
		if !ack.Duplicate {
			t.Fatal("retry not deduplicated by broker")
		}
		return nil
	}
	if _, err = s.PublishBatch(context.Background(), publish); err == nil {
		t.Fatal("crash not injected")
	}
	if _, err = s.PublishBatch(context.Background(), publish); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || count(t, s, "orders") != 1 || count(t, s, "trades") != 0 {
		t.Fatal("duplicate financial effect")
	}
	var pending int
	if err = s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM outbox WHERE published_at IS NULL").Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
}
