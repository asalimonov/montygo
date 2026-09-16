package montygo

import "github.com/asalimonov/montygo/supervisor/docker"

// The Docker supervisor. The implementation lives in montygo/supervisor/docker;
// these are the spellings this package has always used.

// DockerOptions configure the monty-server container montygo runs.
type DockerOptions = docker.Options

// DockerSupervisor runs monty-server in a container on the local Docker daemon.
type DockerSupervisor = docker.Supervisor

// DefaultDockerImage is the repository montygo pulls monty-server from.
const DefaultDockerImage = docker.DefaultImage

// Variables that override the image of a Docker pool.
const (
	DockerImageEnv   = docker.ImageEnv
	DockerVersionEnv = docker.VersionEnv
)

// NewDocker starts a monty-server container and returns a pool that owns it.
var NewDocker = docker.NewPool

// NewDockerSupervisor starts a monty-server container without a pool.
var NewDockerSupervisor = docker.New
