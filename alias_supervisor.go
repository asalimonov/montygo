package montygo

import "github.com/asalimonov/montygo/supervisor"

// The supervisor contract an application implements. Implementations live in
// montygo/supervisor and its subpackages; this package re-exports only the
// contract, not the machinery.

// ServerEndpoint is where a pool dials one attempt.
type ServerEndpoint = supervisor.ServerEndpoint

// ServerSupervisor owns where monty-server runs.
type ServerSupervisor = supervisor.ServerSupervisor

// RecoveryPolicy bounds how a supervised pool retries a dial.
type RecoveryPolicy = supervisor.RecoveryPolicy

// OrphanReaper removes servers left behind by a process that ended without
// closing its supervisor.
type OrphanReaper = supervisor.OrphanReaper

// ErrSupervisorClosed reports a supervisor that no longer serves endpoints.
var ErrSupervisorClosed = supervisor.ErrSupervisorClosed
