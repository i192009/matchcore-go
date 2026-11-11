#!/bin/sh
set -eu
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.1
protoc -I api/proto --go_out=api/gen --go_opt=paths=source_relative --go-grpc_out=api/gen --go-grpc_opt=paths=source_relative api/proto/matchcore/v1/exchange.proto
