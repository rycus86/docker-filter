package connect

import (
	"context"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"net"
	"net/http"
	"strings"
	"testing"
)

var integrationTestCases = map[string]func(*testing.T){
	"Version":                   testIntegrationVersion,
	"ContainerCreateWithLabels": testIntegrationContainerCreateWithLabels,
	"ContainerCreateRefused":    testIntegrationContainerCreateRefused,
	"RefuseDockerExec":          testIntegrationRefuseDockerExec,
}

func testIntegrationVersion(t *testing.T) {
	var capturedRequests []string

	integrationProxy.Handle("/version", func(req *http.Request, body []byte) (*http.Request, error) {
		capturedRequests = append(capturedRequests, req.URL.Path)
		return nil, nil
	})

	if version, err := integrationClient.ServerVersion(context.Background()); err != nil {
		t.Error("Failed to get the server version:", err)
	} else if version.Version == "" {
		t.Errorf("Missing version: %+v", version)
	}
	if len(capturedRequests) != 1 {
		t.Error("Unexpected requests:", capturedRequests)
	}
}

func testIntegrationContainerCreateWithLabels(t *testing.T) {
	type configWrapper struct {
		*container.Config
		HostConfig       *container.HostConfig
		NetworkingConfig *network.NetworkingConfig
	}

	integrationProxy.Handle("/containers/create",
		FilterAsJson(
			func() T { return &configWrapper{} },
			func(r T) T {
				req := r.(*configWrapper)

				if req.Config.Labels == nil {
					req.Config.Labels = map[string]string{}
				}
				req.Config.Labels["docker.filter.added"] = "integration-test"

				return req
			}))

	created, err := integrationClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image: "alpine",
			Cmd:   strslice.StrSlice{"ls"},
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"",
	)
	if err != nil {
		t.Fatal("Failed to create a container:", err)
	}
	defer integrationClient.ContainerRemove(
		context.Background(),
		created.ID,
		types.ContainerRemoveOptions{Force: true})

	ctr, err := integrationClient.ContainerInspect(context.Background(), created.ID)
	if err != nil {
		t.Error("Failed to inspect container", ctr, ":", err)
	}
	if ctr.Config == nil || ctr.Config.Labels == nil {
		t.Errorf("Missing label config on %+v", ctr)
	} else if value := ctr.Config.Labels["docker.filter.added"]; value != "integration-test" {
		t.Errorf("Unexpected labels: %+v", ctr.Config.Labels)
	}
}

func testIntegrationContainerCreateRefused(t *testing.T) {
	type configWrapper struct {
		*container.Config
		HostConfig       *container.HostConfig
		NetworkingConfig *network.NetworkingConfig
	}

	integrationProxy.Handle("/containers/create",
		FilterAsJson(
			func() T { return &configWrapper{} },
			func(r T) T {
				req := r.(*configWrapper)

				hasLabel := func(name string) bool {
					if req.Labels == nil {
						return false
					} else {
						_, ok := req.Labels[name]
						return ok
					}
				}

				if !hasLabel("service.team") {
					panic(NewCriticalFailure("Missing service team label", "Audit"))
				}
				if !hasLabel("service.key") {
					panic(NewCriticalFailure("Missing service key label", "Audit"))
				}
				if !hasLabel("build.number") {
					panic(NewSoftFailure("Missing build number", "BuildWarning"))
				}

				return req
			}))

	SetLogLevel(LogLevel_NONE)

	c1, err := integrationClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image: "alpine",
			Cmd:   strslice.StrSlice{"ls"},
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"",
	)
	if c1.ID != "" {
		integrationClient.ContainerRemove(
			context.Background(),
			c1.ID,
			types.ContainerRemoveOptions{Force: true})

		t.Error("Expected to fail (1)")
	} else if !strings.Contains(err.Error(), "service team") {
		t.Error("Unexpected error message:", err)
	}

	c2, err := integrationClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image: "alpine",
			Cmd:   strslice.StrSlice{"ls"},
			Labels: map[string]string{
				"service.team": "Example Team",
			},
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"",
	)
	if c2.ID != "" {
		integrationClient.ContainerRemove(
			context.Background(),
			c2.ID,
			types.ContainerRemoveOptions{Force: true})

		t.Error("Expected to fail (2)")
	} else if !strings.Contains(err.Error(), "service key") {
		t.Error("Unexpected error message:", err)
	}

	c3, err := integrationClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image: "alpine",
			Cmd:   strslice.StrSlice{"ls"},
			Labels: map[string]string{
				"service.team": "Example Team",
				"service.key":  "SVC-123",
			},
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"",
	)
	if err != nil {
		t.Error("Unexpected failure:", err)
	}
	integrationClient.ContainerRemove(
		context.Background(),
		c3.ID,
		types.ContainerRemoveOptions{Force: true})
}

func testIntegrationRefuseDockerExec(t *testing.T) {
	integrationProxy.Handle("/containers/.+/exec",
		func(req *http.Request, body []byte) (*http.Request, error) {
			return nil, NewCriticalFailure(
				"Not allowed to execute commands in running containers", "Security")
		})

	created, err := integrationClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image:     "alpine",
			Cmd:       strslice.StrSlice{"cat"},
			OpenStdin: true,
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"",
	)
	if err != nil {
		t.Fatal("Failed to create a container:", err)
	}
	defer integrationClient.ContainerRemove(
		context.Background(),
		created.ID,
		types.ContainerRemoveOptions{Force: true})

	if err := integrationClient.ContainerStart(
		context.Background(),
		created.ID,
		types.ContainerStartOptions{}); err != nil {

		t.Error("Failed to start the container:", err)
	}

	SetLogLevel(LogLevel_NONE)

	if _, err := integrationClient.ContainerExecCreate(
		context.Background(),
		created.ID,
		types.ExecConfig{
			Cmd: []string{"echo"},
		},
	); err == nil {
		t.Fatal("Expected to fail execution")
	} else if !strings.Contains(err.Error(), "Not allowed to execute commands") {
		t.Error("Unexpected error message:", err)
	}
}

var (
	integrationDaemonHost string
	integrationClient     *client.Client
	integrationListener   net.Listener
	integrationProxy      *Proxy
)

func onDockerIntegrationSetup() error {
	SetLogLevel(LogLevel_WARN)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	integrationListener = listener

	proxy := NewProxy(func() (net.Conn, error) {
		addressParts := strings.Split(integrationDaemonHost, "://")
		return net.Dial(addressParts[0], addressParts[1])
	})
	proxy.AddListener("test", listener)

	go proxy.Process()

	integrationProxy = proxy

	cli, err := client.NewClientWithOpts(client.WithHost("tcp://" + listener.Addr().String()))
	if err != nil {
		return err
	}
	integrationClient = cli

	return nil
}

func onDockerIntegrationTearDown() {
	if integrationClient != nil {
		integrationClient.Close()
	}
	if integrationListener != nil {
		integrationListener.Close()
	}
}

func TestDockerIntegration(t *testing.T) {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		t.Skip("Can not run integration tests:", err)
	}
	defer cli.Close()

	if version, err := cli.ServerVersion(context.Background()); err != nil {
		t.Skip("Can not run integration tests:", err)
	} else {
		t.Logf(
			"Running Docker integration tests against version: %s (API: %s)",
			version.Version, version.APIVersion)
	}

	integrationDaemonHost = cli.DaemonHost()

	for name, testFunc := range integrationTestCases {
		if err := onDockerIntegrationSetup(); err != nil {
			panic(err)
		}

		t.Run(name, testFunc)

		onDockerIntegrationTearDown()
	}
}
