package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
	"github.com/docker/go-units"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func testDockerContainerCreate(t *testing.T) {
	type configWrapper struct {
		*container.Config
		HostConfig       *container.HostConfig
		NetworkingConfig *network.NetworkingConfig
	}

	dockerRequestProcessors["/containers/create"] = func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/containers/create") {
			t.Errorf("Unexpected request path: %s", r.URL.Path)
		}

		var body configWrapper
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal("Failed to decode the JSON body:", err)
		}

		if body.Image != "test-image" {
			t.Errorf("Unexpected image: %s", body.Image)
		}

		if body.Hostname != "filter.host" {
			t.Errorf("Unexpected hostname: %s", body.Hostname)
		}

		if !body.HostConfig.NetworkMode.IsContainer() || body.HostConfig.NetworkMode.ConnectedContainer() != "x" {
			t.Errorf("Unexpected network mode: %s", body.HostConfig.NetworkMode)
		}

		if !body.HostConfig.PidMode.IsHost() {
			t.Errorf("Unexpected pid mode: %s", body.HostConfig.PidMode)
		}

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(&container.ContainerCreateCreatedBody{ID: "abcd1234"})
	}

	dockerProxy.Handle("/containers/create",
		FilterAsJson(
			func() T { return &configWrapper{} },
			func(r T) T {
				req := r.(*configWrapper)

				if req.Config == nil || req.HostConfig == nil {
					t.Fatal("Failed to convert the request body")
				}

				req.Config.Hostname = "filter.host"
				req.HostConfig.PidMode = container.PidMode("host")
				return req
			}))

	if successfulResponse, err := dockerClient.ContainerCreate(
		context.Background(),
		&container.Config{
			Image: "test-image",
		},
		&container.HostConfig{
			NetworkMode: container.NetworkMode("container:x"),
		},
		&network.NetworkingConfig{},
		"testing",
	); err != nil {
		t.Error("Failed to simulate service create:", err)
	} else if successfulResponse.ID != "abcd1234" {
		t.Errorf("Unexpected response: %+v", successfulResponse)
	}

	if dockerRequestCount != 1 {
		t.Errorf("Unexpected number of requests: %d", dockerRequestCount)
	}
}

func testDockerServiceCreate(t *testing.T) {
	dockerRequestProcessors["/services/create"] = func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/services/create") {
			t.Errorf("Unexpected request path: %s", r.URL.Path)
		}

		var body swarm.ServiceSpec
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal("Failed to decode the JSON body:", err)
		}

		if body.TaskTemplate.ContainerSpec.Image != "swarm-image:v1" {
			t.Errorf("Unexpected image: %s", body.TaskTemplate.ContainerSpec.Image)
		}

		if body.Name != "test-svc" {
			t.Errorf("Unexpected name: %s", body.Name)
		}

		if body.Labels["docker.filter.applied"] != "1" {
			t.Error("Missing labels:", body.Labels)
		}

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(&types.ServiceCreateResponse{
			ID: "created.svc",
		})
	}

	var (
		memMaxLimit, _ = units.RAMInBytes("512M")
		mem64M, _      = units.RAMInBytes("64M")
	)

	dockerProxy.Handle("/services/create",
		FilterAsJson(
			func() T { return &swarm.ServiceSpec{} },
			func(r T) T {
				req := r.(*swarm.ServiceSpec)

				if strings.Contains(req.TaskTemplate.ContainerSpec.Image, ":latest") {
					panic(NewCriticalFailure("do not use the latest tag", "Policy"))
				}

				if req.TaskTemplate.Resources == nil ||
					req.TaskTemplate.Resources.Limits == nil ||
					req.TaskTemplate.Resources.Limits.MemoryBytes <= 0 ||
					req.TaskTemplate.Resources.Limits.MemoryBytes > memMaxLimit {

					panic(NewCriticalFailure("missing or too high memory limits", "Resources"))
				}

				if req.Labels == nil {
					req.Labels = map[string]string{}
				}

				req.Labels["docker.filter.applied"] = "1"

				return req
			}))

	successfulResponse, err := dockerClient.ServiceCreate(
		context.Background(),
		swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Image: "swarm-image:v1",
				},
				Resources: &swarm.ResourceRequirements{
					Limits: &swarm.Resources{
						MemoryBytes: mem64M,
					},
				},
			},
			Annotations: swarm.Annotations{
				Name: "test-svc",
			},
		},
		types.ServiceCreateOptions{},
	)
	if err != nil {
		t.Error("Failed to simulate service create:", err)
	}
	if successfulResponse.ID != "created.svc" {
		t.Error("Unexpected ID in response:", successfulResponse.ID)
	}

	SetLogLevel(LogLevel_NONE)

	if _, err := dockerClient.ServiceCreate(
		context.Background(),
		swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Image: "no-tag",
				},
			},
		},
		types.ServiceCreateOptions{},
	); err != nil {
		if !strings.Contains(err.Error(), "do not use the latest tag") {
			t.Error("Unexpected failure:", err)
		}
	} else {
		t.Error("Failing request (1) was successful")
	}

	if _, err := dockerClient.ServiceCreate(
		context.Background(),
		swarm.ServiceSpec{
			TaskTemplate: swarm.TaskSpec{
				ContainerSpec: &swarm.ContainerSpec{
					Image: "no-limits:test",
				},
			},
		},
		types.ServiceCreateOptions{},
	); err != nil {
		if !strings.Contains(err.Error(), "missing or too high memory limits") {
			t.Error("Unexpected failure:", err)
		}
	} else {
		t.Error("Failing request (2) was successful")
	}

	if dockerRequestCount != 1 {
		t.Errorf("Unexpected number of requests: %d", dockerRequestCount)
	}
}

func testDockerServiceUpdate(t *testing.T) {
	dockerRequestProcessors["/"] = func(w http.ResponseWriter, r *http.Request) {
		if !regexp.MustCompile("/services/.+/update").MatchString(r.URL.Path) {
			t.Errorf("Unexpected request path: %s", r.URL.Path)
		}

		var body swarm.ServiceSpec
		json.NewDecoder(r.Body).Decode(&body)

		if body.Labels["docker.filter.applied"] != "1" {
			t.Error("Missing labels:", body.Labels)
		}

		w.WriteHeader(200)
		json.NewEncoder(w).Encode(&types.ServiceUpdateResponse{})
	}

	dockerProxy.Handle("/services/.+/update", func(req *http.Request, body []byte) (*http.Request, error) {
		if req.URL.Query().Get("rollback") != "previous" {
			panic(NewCriticalFailure("no or invalid rollback policy set", "Policy"))
		}
		return nil, nil
	})

	dockerProxy.Handle("/services/.+/update", func(httpReq *http.Request, body []byte) (*http.Request, error) {
		return FilterAsJson(
			func() T { return &swarm.ServiceSpec{} },
			func(r T) T {
				req := r.(*swarm.ServiceSpec)

				if req.Mode.Replicated != nil {
					if req.Mode.Replicated.Replicas == nil ||
						*req.Mode.Replicated.Replicas < 3 {

						serviceId := regexp.
							MustCompile(".*/services/(.+)/update.*").
							ReplaceAllString(httpReq.URL.Path, "$1")

						panic(NewSoftFailure(fmt.Sprintf(
							"consider running at least 3 replicas of the '%s' service", serviceId),
							"Advice"))
					}
				}

				return req
			})(httpReq, body)
	})

	dockerProxy.Handle("/services/.+/update",
		FilterAsJson(
			func() T { return &swarm.ServiceSpec{} },
			func(r T) T {
				req := r.(*swarm.ServiceSpec)

				if req.Labels == nil {
					req.Labels = map[string]string{}
				}

				req.Labels["docker.filter.applied"] = "1"

				return req
			}))

	SetLogLevel(LogLevel_NONE)

	two := uint64(2)
	successfulResponse, err := dockerClient.ServiceUpdate(
		context.Background(),
		"to_update",
		swarm.Version{Index: 12},
		swarm.ServiceSpec{
			Mode: swarm.ServiceMode{
				Replicated: &swarm.ReplicatedService{
					Replicas: &two,
				},
			},
		},
		types.ServiceUpdateOptions{
			Rollback: "previous",
		},
	)
	if err != nil || len(successfulResponse.Warnings) > 0 {
		t.Error("Failed to update service:", err)
	}

	if _, err := dockerClient.ServiceUpdate(
		context.Background(),
		"failing",
		swarm.Version{Index: 13},
		swarm.ServiceSpec{},
		types.ServiceUpdateOptions{},
	); err != nil {
		if !strings.Contains(err.Error(), "rollback policy") {
			t.Error("Unexpected failure:", err)
		}
	} else {
		t.Error("Failing request was successful")
	}
}

var (
	dockerClient   *client.Client
	dockerServer   *httptest.Server
	dockerListener net.Listener
	dockerProxy    *Proxy

	dockerRequestProcessors = map[string]dockerRequestProcessor{}
	dockerRequestCount      int
)

type dockerRequestProcessor func(w http.ResponseWriter, r *http.Request)

func onDockerSetup() error {
	SetLogLevel(LogLevel_WARN)

	dockerRequestCount = 0

	for k := range dockerRequestProcessors {
		delete(dockerRequestProcessors, k)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for pattern, process := range dockerRequestProcessors {
			if regexp.MustCompile(pattern).MatchString(r.URL.Path) {
				process(w, r)
				dockerRequestCount += 1
			}
		}
	}))
	dockerServer = server

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	dockerListener = listener

	proxy := NewProxy(func() (net.Conn, error) {
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

func TestDockerMessages(t *testing.T) {
	cases := map[string]func(*testing.T){
		"ContainerCreate": testDockerContainerCreate,
		"ServiceCreate":   testDockerServiceCreate,
		"ServiceUpdate":   testDockerServiceUpdate,
	}

	for name, testFunc := range cases {
		if err := onDockerSetup(); err != nil {
			panic(err)
		}

		t.Run(name, testFunc)

		onDockerTearDown()
	}
}
