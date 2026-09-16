package engine

import host "github.com/asalimonov/montygo/runtime/host"

// Types.
type AttrError = host.AttrError
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
type InstanceStore = host.InstanceStore
type Kwargs = host.Kwargs
type MethodProvider = host.MethodProvider
type OSHandler = host.OSHandler
type Wrapper = host.Wrapper

// Variables and sentinels.
var ErrAttrNotExposed = host.ErrAttrNotExposed
var ErrHostObjectNotRestorable = host.ErrHostObjectNotRestorable
var NotHandled = host.NotHandled

// Functions.
var All = host.All
var AsNamedTuple = host.AsNamedTuple
var Async = host.Async
var AsyncContext = host.AsyncContext
var BlockRegistration = host.BlockRegistration
var CallWrapperMethod = host.CallWrapperMethod
var Func = host.Func
var KwargsRecord = host.KwargsRecord
var MustClassInstance = host.MustClassInstance
var MustFunc = host.MustFunc
var Names = host.Names
var NewClassInstance = host.NewClassInstance
var NewClassTypeOf = host.NewClassTypeOf
var NewFuture = host.NewFuture
var NewHost = host.NewHost
var NewInstanceStore = host.NewInstanceStore
var NewNamedTuple = host.NewNamedTuple
var PrepareValue = host.PrepareValue
var RestoreValue = host.RestoreValue
var SandboxName = host.SandboxName
var TypeName = host.TypeName
var WrapperLazyAttr = host.WrapperLazyAttr
var WrapperName = host.WrapperName
