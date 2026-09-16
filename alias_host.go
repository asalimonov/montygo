package montygo

import host "github.com/asalimonov/montygo/runtime/host"

// Types.
type AttrLister = host.AttrLister
type AttrPolicy = host.AttrPolicy
type AttrProvider = host.AttrProvider
type ClassInstance = host.ClassInstance
type ClassInstanceOptions = host.ClassInstanceOptions
type ClassProxy = host.ClassProxy
type ClassType = host.ClassType
type ClassTypeOptions = host.ClassTypeOptions
type Function = host.Function
type FunctionFunc = host.FunctionFunc
type Future = host.Future
type Host = host.Host
type HostFuncOptions = host.HostFuncOptions
type Kwargs = host.Kwargs
type MethodProvider = host.MethodProvider
type OSHandler = host.OSHandler

// Variables and sentinels.
var ErrAttrNotExposed = host.ErrAttrNotExposed
var ErrHostObjectNotRestorable = host.ErrHostObjectNotRestorable
var NotHandled = host.NotHandled

// Functions.
var All = host.All
var AsNamedTuple = host.AsNamedTuple
var Async = host.Async
var AsyncContext = host.AsyncContext
var Func = host.Func
var MustClassInstance = host.MustClassInstance
var MustFunc = host.MustFunc
var Names = host.Names
var NewClassInstance = host.NewClassInstance
var NewClassTypeOf = host.NewClassTypeOf
var NewFuture = host.NewFuture
var NewHost = host.NewHost
var NewNamedTuple = host.NewNamedTuple
var SandboxName = host.SandboxName
var TypeName = host.TypeName
