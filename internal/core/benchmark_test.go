package core

import (
	"encoding/json"
	"fmt"
	"testing"
)

func fixture(b *testing.B, n int) *Book {
	b.Helper()
	book := NewBook("BTC-USD")
	for i := 0; i < n; i++ {
		o := order(fmt.Sprintf("s%d", i), Sell, int64(100+i%1000), 100)
		apply(b, book, o)
	}
	return book
}
func BenchmarkBook(b *testing.B) {
	for _, size := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			for _, scenario := range []string{"Insert", "Match", "Partial", "Cancel", "Sweep", "Snapshot", "Replay"} {
				b.Run(scenario, func(b *testing.B) {
					book := fixture(b, size)
					b.ReportAllocs()
					b.ResetTimer()
					for n := 0; n < b.N; n++ {
						switch scenario {
						case "Insert":
							o := order(fmt.Sprintf("b%d", n), Buy, 50, 1)
							p, e := book.Plan(o, book.Sequence+1, 1)
							if e != nil {
								b.Fatal(e)
							}
							if e = book.Apply(p, book.Sequence+1); e != nil {
								b.Fatal(e)
							}
							b.StopTimer()
							c, _ := book.Cancel(o.ID, o.Account, 1)
							_ = book.Apply(c, book.Sequence+1)
							b.StartTimer()
						case "Match", "Partial", "Sweep":
							q := int64(100)
							if scenario == "Partial" {
								q = 1
							}
							if scenario == "Sweep" {
								q = 1000
							}
							o := order("take", Buy, 2000, q)
							o.TIF = "IOC"
							p, e := book.Plan(o, book.Sequence+1, 1)
							if e != nil {
								b.Fatal(e)
							}
							if e = book.Apply(p, book.Sequence+1); e != nil {
								b.Fatal(e)
							}
							b.StopTimer()
							for _, ch := range p.Changes {
								if ch.Before != nil {
									book.erase(ch.Before.ID)
									book.add(*ch.Before)
								}
							}
							b.StartTimer()
						case "Cancel":
							b.StopTimer()
							o := order("cancel", Buy, 50, 1)
							apply(b, book, o)
							b.StartTimer()
							p, e := book.Cancel(o.ID, o.Account, 1)
							if e != nil {
								b.Fatal(e)
							}
							if e = book.Apply(p, book.Sequence+1); e != nil {
								b.Fatal(e)
							}
						case "Snapshot":
							if _, e := json.Marshal(book.Orders()); e != nil {
								b.Fatal(e)
							}
						case "Replay":
							b.StopTimer()
							o := order("replay", Buy, 50, 1)
							p, e := book.Plan(o, book.Sequence+1, 1)
							if e != nil {
								b.Fatal(e)
							}
							b.StartTimer()
							if e = book.Apply(p, book.Sequence+1); e != nil {
								b.Fatal(e)
							}
							b.StopTimer()
							book.erase(o.ID)
							b.StartTimer()
						}
					}
				})
			}
		})
	}
}

// BenchmarkEventReplay includes JSON decoding and application of accepted-order
// event plans. SQL I/O and financial settlement are intentionally excluded.
func BenchmarkEventReplay(b *testing.B) {
	for _, size := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			source := NewBook("BTC-USD")
			events := make([][]byte, size)
			var bytes int64
			for n := 0; n < size; n++ {
				p := apply(b, source, order(fmt.Sprintf("replay-%d", n), Sell, int64(100+n%1000), 1))
				data, e := json.Marshal(p)
				if e != nil {
					b.Fatal(e)
				}
				events[n] = data
				bytes += int64(len(data))
			}
			b.ReportAllocs()
			b.SetBytes(bytes)
			b.ReportMetric(float64(size), "events/op")
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				recovered := NewBook("BTC-USD")
				for idx, data := range events {
					var p Plan
					if e := json.Unmarshal(data, &p); e != nil {
						b.Fatal(e)
					}
					if e := recovered.Apply(p, int64(idx+1)); e != nil {
						b.Fatal(e)
					}
				}
				if recovered.Len() != size {
					b.Fatal("incomplete replay")
				}
			}
		})
	}
}
