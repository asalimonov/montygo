// Package montypb holds the wire types generated from proto/monty/v1/monty.proto.
//
// They carry no stability promise beyond montygo.ProtocolVersion; consumers
// speaking the raw protocol MAY import them and MUST expect regeneration on
// every protocol change. The runtime codec in internal/wire does not use them.
package montypb

//go:generate protoc -I ../proto --go_out=. --go_opt=module=github.com/asalimonov/montygo/montypb --go_opt=Mmonty/v1/monty.proto=github.com/asalimonov/montygo/montypb ../proto/monty/v1/monty.proto
