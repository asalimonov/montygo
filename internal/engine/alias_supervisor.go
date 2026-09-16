package engine

import supervisor "github.com/asalimonov/montygo/supervisor"

// Types.
type NoopReaper = supervisor.NoopReaper
type OrphanReaper = supervisor.OrphanReaper
type Recoverer = supervisor.Recoverer
type RecoveryPolicy = supervisor.RecoveryPolicy
type ServerEndpoint = supervisor.ServerEndpoint
type ServerSupervisor = supervisor.ServerSupervisor
type Static = supervisor.Static

// Variables and sentinels.
var ErrSupervisorClosed = supervisor.ErrSupervisorClosed

// Functions.
var NewRecoverer = supervisor.NewRecoverer
var NewStatic = supervisor.NewStatic
var SortedHeaders = supervisor.SortedHeaders
