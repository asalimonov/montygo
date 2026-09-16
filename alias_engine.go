package montygo

import eng "github.com/asalimonov/montygo/internal/engine"

// Types.
type Backend = eng.Backend
type CheckoutOptions = eng.CheckoutOptions
type Complete = eng.Complete
type FeedOptions = eng.FeedOptions
type FunctionSnapshot = eng.FunctionSnapshot
type FutureResolution = eng.FutureResolution
type FutureSnapshot = eng.FutureSnapshot
type LoadSnapshotOptions = eng.LoadSnapshotOptions
type NameLookupSnapshot = eng.NameLookupSnapshot
type Options = eng.Options
type Pool = eng.Pool
type PoolStats = eng.PoolStats
type ResourceLimits = eng.ResourceLimits
type Run = eng.Run
type RunOptions = eng.RunOptions
type ServerInfo = eng.ServerInfo
type ServerLimits = eng.ServerLimits
type Session = eng.Session
type SessionState = eng.SessionState
type SessionStats = eng.SessionStats
type Slot = eng.Slot
type Snapshot = eng.Snapshot
type SpawnError = eng.SpawnError
type StopKind = eng.StopKind
type StopPolicy = eng.StopPolicy
type Stopped = eng.Stopped
type TypeCheckFormat = eng.TypeCheckFormat
type WebSocketOptions = eng.WebSocketOptions

// Constants.
const BackendAuto = eng.BackendAuto
const BackendDocker = eng.BackendDocker
const BackendNative = eng.BackendNative
const BackendWasm = eng.BackendWasm
const BackendWebSocket = eng.BackendWebSocket
const FormatAzure = eng.FormatAzure
const FormatConcise = eng.FormatConcise
const FormatFull = eng.FormatFull
const FormatGitHub = eng.FormatGitHub
const FormatGitLab = eng.FormatGitLab
const FormatJSON = eng.FormatJSON
const FormatJSONLines = eng.FormatJSONLines
const FormatPylint = eng.FormatPylint
const FormatRDJSON = eng.FormatRDJSON
const NoDurationLimitGrace = eng.NoDurationLimitGrace
const NoRequestTimeout = eng.NoRequestTimeout
const SessionClosed = eng.SessionClosed
const SessionIdle = eng.SessionIdle
const SessionPaused = eng.SessionPaused
const SessionRunning = eng.SessionRunning
const StopAborted = eng.StopAborted
const StopFinished = eng.StopFinished
const StopKilled = eng.StopKilled
const StopNotRunning = eng.StopNotRunning
const StopPending = eng.StopPending
const Unlimited = eng.Unlimited
const UnlimitedDuration = eng.UnlimitedDuration
const UnlimitedPendingBytes = eng.UnlimitedPendingBytes

// Variables and sentinels.
var DefaultStopPolicy = eng.DefaultStopPolicy
var ErrCallbackDetached = eng.ErrCallbackDetached
var ErrNoServerInfo = eng.ErrNoServerInfo
var KillNow = eng.KillNow

// Functions.
var CheckWebSocketHealth = eng.CheckWebSocketHealth
var DurationPtr = eng.DurationPtr
var FetchServerInfo = eng.FetchServerInfo
var FindMontyBinary = eng.FindMontyBinary
var New = eng.New
var NewWebSocket = eng.NewWebSocket
var Uint32 = eng.Uint32
