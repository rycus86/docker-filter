package example

import (
	"context"
	"encoding/json"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/rycus86/docker-filter/pkg/connect"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestDockerMessages(t *testing.T) {
	type configWrapper struct {
		*container.Config
		HostConfig       *container.HostConfig
		NetworkingConfig *network.NetworkingConfig
	}

	requestProcessors["/.*"] = func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/create") {
			t.Errorf("Unexpected request path: %s", r.URL.Path)
		}

		var body configWrapper
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal("Failed to decode the JSON body:", err)
		}

		if body.Hostname != "filter.host" {
			t.Errorf("Unexpected hostname: %s", body.Hostname)
		}
	}

	dockerProxy.Handle("/.*",
		connect.FilterAsJson(
			func() connect.T { return &configWrapper{} },
			func(t connect.T) connect.T {
				req := t.(*configWrapper)
				req.Config.Hostname = "filter.host"
				return req
			}))

	dockerClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image: "test-image",
		},
		&container.HostConfig{},
		&network.NetworkingConfig{},
		"testing",
	)
}

var (
	dockerClient   *client.Client
	dockerServer   *httptest.Server
	dockerListener net.Listener
	dockerProxy    *connect.Proxy

	requestProcessors = map[string]dockerRequestProcessor{}
)

type dockerRequestProcessor func(w http.ResponseWriter, r *http.Request)

func onDockerSetup() error {
	connect.SetLogLevel(connect.LogLevel_WARN)

	for k := range requestProcessors {
		delete(requestProcessors, k)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for pattern, process := range requestProcessors {
			if regexp.MustCompile(pattern).MatchString(r.URL.Path) {
				process(w, r)
			}
		}
	}))
	dockerServer = server

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	dockerListener = listener

	proxy := connect.NewProxy(func() (net.Conn, error) {
		return net.Dial(server.Listener.Addr().Network(), server.Listener.Addr().String())
	})
	proxy.AddListener("test", listener)

	go proxy.Process()

	dockerProxy = proxy

	cli, err := client.NewClientWithOpts(
		client.WithHTTPClient(server.Client()),
		client.WithHost("tcp://"+listener.Addr().String()),
	)
	if err != nil {
		return err
	}
	dockerClient = cli

	return nil
}

func onDockerTearDown() {
	if dockerClient != nil {
		dockerClient.Close()
	}
	if dockerListener != nil {
		dockerListener.Close()
	}
	if dockerServer != nil {
		dockerServer.Close()
	}
}

func TestMain(m *testing.M) {
	if err := onDockerSetup(); err != nil {
		panic(err)
	}

	result := m.Run()
	onDockerTearDown()
	os.Exit(result)
}
