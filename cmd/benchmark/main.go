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
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/i192009/matchcore-go/internal/core"
	"github.com/i192009/matchcore-go/internal/engine"
	"github.com/i192009/matchcore-go/internal/store"
)

type report struct {
	Mode            string   `json:"mode"`
	Scenario        string   `json:"scenario"`
	Go              string   `json:"go"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	CPUs            int      `json:"cpus"`
	Orders          int      `json:"orders"`
	Trades          int64    `json:"trades"`
	Concurrency     int      `json:"concurrency"`
	Resting         int      `json:"resting"`
	Crossing        int      `json:"crossing_percent"`
	Seconds         float64  `json:"seconds"`
	OrdersPerSecond float64  `json:"orders_per_second"`
	TradesPerSecond float64  `json:"trades_per_second"`
	P50             float64  `json:"p50_us"`
	P95             float64  `json:"p95_us"`
	P99             float64  `json:"p99_us"`
	Allocations     uint64   `json:"allocations"`
	Bytes           uint64   `json:"bytes_allocated"`
	Heap            uint64   `json:"heap_bytes"`
	GC              uint32   `json:"gc_cycles"`
	GCPause         uint64   `json:"gc_pause_ns"`
	Failures        int64    `json:"failures"`
	CPUSeconds      *float64 `json:"process_cpu_seconds"`
	CPUPercent      *float64 `json:"process_cpu_percent"`
}

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "memory", "memory, durable, or e2e")
	scenario := flag.String("scenario", "single", "single, multi, or contention")
	n := flag.Int("n", 10000, "measured orders")
	resting := flag.Int("resting", 1000, "fixture orders per instrument")
	crossing := flag.Int("crossing", 50, "crossing percentage")
	concurrency := flag.Int("concurrency", 1, "parallel clients (durable/e2e)")
	url := flag.String("url", "http://localhost:8080", "HTTP gateway")
	runID := flag.String("run", fmt.Sprint(time.Now().UnixNano()), "unique command namespace")
	flag.Parse()
	if *n < 1 || *resting < 1 || *resting > 100000 || *concurrency < 1 || *crossing < 0 || *crossing > 100 {
		return fmt.Errorf("invalid benchmark parameters")
	}
	ctx := context.Background()
	var db *store.Store
	var eng *engine.Engine
	var err error
	ids := []string{"BTC-USD"}
	if *scenario == "multi" || *scenario == "contention" {
		ids = []string{"BTC-USD", "ETH-USD", "SOL-USD"}
	}
	books := map[string]*core.Book{}
	for _, id := range ids {
		books[id] = core.NewBook(id)
	}
	command := func(account, key, id string, side core.Side, p, q int64) store.Command {
		o := core.Order{ID: account + "-" + key, ClientID: key, Account: account, Instrument: id, Side: side, Type: "LIMIT", TIF: "GTC", Price: p, Quantity: q}
		return store.Command{Kind: "place", Account: account, Key: key, Instrument: id, Order: o}
	}
	if *mode == "durable" {
		db, err = store.Open(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			return err
		}
		defer db.Pool.Close()
		eng, err = engine.New(ctx, db, 4096)
		if err != nil {
			return err
		}
		defer eng.Close()
	}
	client := http.Client{Timeout: 30 * time.Second}
	buyKey, sellKey := os.Getenv("SIM_BUY_KEY"), os.Getenv("SIM_SELL_KEY")
	execute := func(c store.Command) (int, error) {
		switch *mode {
		case "memory":
			b := books[c.Instrument]
			p, e := b.Plan(c.Order, b.Sequence+1, int64(1))
			if e != nil {
				return 0, e
			}
			e = b.Apply(p, b.Sequence+1)
			return len(p.Trades), e
		case "durable":
			env, e := eng.Submit(ctx, c)
			if e == nil && env.Error != "" {
				e = fmt.Errorf("%s", env.Error)
			}
			return len(env.Plan.Trades), e
		case "e2e":
			key := buyKey
			if c.Account == "bob" {
				key = sellKey
			}
			body := map[string]any{"client_id": c.Key, "instrument": c.Instrument, "side": c.Order.Side, "type": c.Order.Type, "tif": c.Order.TIF, "price": c.Order.Price, "quantity": c.Order.Quantity}
			data, _ := json.Marshal(body)
			req, e := http.NewRequestWithContext(ctx, "POST", *url+"/api/orders", bytes.NewReader(data))
			if e != nil {
				return 0, e
			}
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			res, e := client.Do(req)
			if e != nil {
				return 0, e
			}
			v, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
			_ = res.Body.Close()
			if e != nil {
				return 0, e
			}
			if res.StatusCode != 200 {
				return 0, fmt.Errorf("HTTP %d: %s", res.StatusCode, v)
			}
			var env store.Envelope
			if e = json.Unmarshal(v, &env); e != nil {
				return 0, e
			}
			return len(env.Plan.Trades), nil
		}
		return 0, fmt.Errorf("unknown mode")
	}
	if *mode == "memory" && *concurrency != 1 {
		return fmt.Errorf("memory benchmark requires concurrency=1 (single writer)")
	}
	if *mode == "e2e" && (buyKey == "" || sellKey == "") {
		return fmt.Errorf("SIM_BUY_KEY and SIM_SELL_KEY required")
	}
	// Fixture inventory covers at least all measured crossing orders. Use fresh demo
	// accounts with enough simulated inventory for the selected data set.
	for _, id := range ids {
		for j := 0; j < *resting; j++ {
			c := command("bob", fmt.Sprintf("%s-fixture-%s-%d", *runID, id, j), id, core.Sell, 10, 1)
			if _, err = execute(c); err != nil {
				return fmt.Errorf("fixture: %w", err)
			}
		}
	}
	latencies := make([]int64, *n)
	var next atomic.Int64
	var failures, trades atomic.Int64
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	cpuBefore, cpuValid := cpuSeconds()
	start := time.Now()
	var wg sync.WaitGroup
	for worker := 0; worker < *concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				j := int(next.Add(1) - 1)
				if j >= *n {
					return
				}
				id := ids[j%len(ids)]
				p := int64(1)
				if j%100 < *crossing {
					p = 10
				}
				c := command("alice", fmt.Sprintf("%s-order-%d", *runID, j), id, core.Buy, p, 1)
				c.Order.TIF = "IOC"
				t := time.Now()
				filled, e := execute(c)
				latencies[j] = time.Since(t).Nanoseconds()
				if e != nil {
					failures.Add(1)
				}
				trades.Add(int64(filled))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	cpuAfter, cpuValidAfter := cpuSeconds()
	runtime.ReadMemStats(&after)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	percentile := func(p int) float64 { return float64(latencies[(*n-1)*p/100]) / 1000 }
	r := report{Mode: *mode, Scenario: *scenario, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Orders: *n, Trades: trades.Load(), Concurrency: *concurrency, Resting: *resting, Crossing: *crossing, Seconds: elapsed, OrdersPerSecond: float64(*n) / elapsed, TradesPerSecond: float64(trades.Load()) / elapsed, P50: percentile(50), P95: percentile(95), P99: percentile(99), Allocations: after.Mallocs - before.Mallocs, Bytes: after.TotalAlloc - before.TotalAlloc, Heap: after.HeapAlloc, GC: after.NumGC - before.NumGC, GCPause: after.PauseTotalNs - before.PauseTotalNs, Failures: failures.Load()}
	if cpuValid && cpuValidAfter {
		seconds := max(cpuAfter-cpuBefore, 0)
		percent := seconds / elapsed * 100
		r.CPUSeconds = &seconds
		r.CPUPercent = &percent
	}
	if err = json.NewEncoder(os.Stdout).Encode(r); err != nil {
		return err
	}
	if failures.Load() > 0 {
		return fmt.Errorf("benchmark had %d failed commands", failures.Load())
	}
	return nil
}

// Linux process CPU counters have CLK_TCK resolution (usually 10ms). Other
// platforms report null; end-to-end mode measures the client process only.
func cpuSeconds() (float64, bool) {
	if runtime.GOOS != "linux" {
		return 0, false
	}
	data, e := os.ReadFile("/proc/self/stat")
	if e != nil {
		return 0, false
	}
	fields := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+1:])
	if len(fields) < 13 {
		return 0, false
	}
	user, e := strconv.ParseFloat(fields[11], 64)
	if e != nil {
		return 0, false
	}
	system, e := strconv.ParseFloat(fields[12], 64)
	if e != nil {
		return 0, false
	}
	output, e := exec.Command("getconf", "CLK_TCK").Output()
	if e != nil {
		return 0, false
	}
	frequency, e := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if e != nil || frequency <= 0 {
		return 0, false
	}
	return (user + system) / frequency, true
}
