// Package montypb holds the protobuf types generated from proto/monty/v1/monty.proto.
package montypb

//go:generate protoc -I ../proto --go_out=. --go_opt=module=github.com/asalimonov/montygo/montypb --go_opt=Mmonty/v1/monty.proto=github.com/asalimonov/montygo/montypb ../proto/monty/v1/monty.proto
