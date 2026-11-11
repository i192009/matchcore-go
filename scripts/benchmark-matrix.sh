#!/bin/sh
set -eu
mkdir -p benchmarks/results
go build -o /tmp/matchcore-benchmark ./cmd/benchmark
for size in 1000 10000 100000; do
  for crossing in 10 50 90; do
    /tmp/matchcore-benchmark --mode memory --n 1000 --resting "$size" --crossing "$crossing" >"benchmarks/results/memory-${size}-${crossing}.json"
  done
done
go test ./internal/core -run '^$' -bench BenchmarkBook -benchmem -benchtime=100x -count=1 > benchmarks/results/go-bench.txt
go test ./internal/core -run '^$' -bench BenchmarkEventReplay -benchmem -benchtime=1x -count=1 > benchmarks/results/replay.txt
