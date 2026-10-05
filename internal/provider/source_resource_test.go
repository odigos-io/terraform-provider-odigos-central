// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// startMockOdigosServerForSource starts an httptest.Server that supports the source resource:
//   - signIn returns a valid access token.
//   - GetSystemConfig reports a Central version >= MinCentralVersion.
//   - GetComputePlatforms returns the supplied JSON list.
//   - RemoteFetch handles GetSource and PersistSources operations.
//
// computePlatformsJSON is the JSON array body of the computePlatforms field.
// Returns the server and hostname (no scheme) for the provider block.
func startMockOdigosServerForSource(t *testing.T, computePlatformsJSON string) (*httptest.Server, string) {
	t.Helper()

	innerSourceResponse := func(namespace, name, kind, otelServiceName string, dataStreamNames []string) string {
		streams, _ := json.Marshal(dataStreamNames)
		return `{"type":"","data":{"computePlatform":{"source":{"namespace":"` + namespace + `","name":"` + name + `","kind":"` + kind + `","dataStreamNames":` + string(streams) + `,"selected":true,"otelServiceName":"` + otelServiceName + `","containers":[],"conditions":[]}}},"request_id":""}`
	}
	emptySourceResponse := `{"type":"","data":{"computePlatform":{"source":{"namespace":"","name":"","kind":"","dataStreamNames":[],"selected":false,"otelServiceName":"","containers":[],"conditions":[]}}},"request_id":""}`
	innerPersistResponse := `{"type":"","data":{"persistK8sSources":true},"request_id":""}`

	var mu sync.Mutex
	persisted := false

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Query     string                 `json:"query"`
			Variables map[string]interface{} `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		query := strings.TrimSpace(body.Query)
		switch {
		case strings.Contains(query, "signIn") || strings.Contains(query, "SignIn"):
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
		case strings.Contains(query, "systemConfig") || strings.Contains(query, "GetSystemConfig"):
			_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.22.0"}},"errors":[]}`))
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + computePlatformsJSON + `},"errors":[]}`))
		case strings.Contains(query, "remoteFetch") || strings.Contains(query, "RemoteFetch"):
			innerQuery, _ := body.Variables["query"].(string)
			var inner string
			switch {
			case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
				mu.Lock()
				done := persisted
				mu.Unlock()
				if done {
					inner = innerSourceResponse("default", "my-deploy", "Deployment", "my-deploy-default", []string{"default"})
				} else {
					inner = emptySourceResponse
				}
			case strings.Contains(innerQuery, "persistK8sSources"):
				mu.Lock()
				persisted = true
				mu.Unlock()
				inner = innerPersistResponse
			default:
				inner = `{"type":"","data":{},"request_id":""}`
			}
			remoteFetchVal, _ := json.Marshal(inner)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(remoteFetchVal) + `},"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))
	hostname := strings.TrimPrefix(server.URL, "https://")
	return server, hostname
}

// onePlatformForSource is a single supported (>= v1.20) compute platform so
// ProxyByName("TestCluster") returns a *ProxyClient bound to "cluster-1".
const onePlatformForSource = `[{"id":"cluster-1","name":"TestCluster","odigosVersion":"v1.22.0","type":"k8s","status":"connected","connectedAt":0,"lastSeenAt":0,"users":[],"teams":[],"__typename":"ComputePlatform"}]`

// onePlatformForSourceTooOld represents a single cluster running an
// unsupported (pre-min) Odigos version. The provider should soft-fail with a
// per-cluster warning and skip the resource.
const onePlatformForSourceTooOld = `[{"id":"cluster-1","name":"TestCluster","odigosVersion":"v1.18.0","type":"k8s","status":"connected","connectedAt":0,"lastSeenAt":0,"users":[],"teams":[],"__typename":"ComputePlatform"}]`

func TestAccOdigosSourceResource_CreateReadDelete(t *testing.T) {
	server, hostname := startMockOdigosServerForSource(t, onePlatformForSource)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOdigosSourceResourceConfig(hostname, "TestCluster", "my-deploy", "Deployment", "default", "default"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odigos-central_source.example", "cluster_name", "TestCluster"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "cluster_id", "cluster-1"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "name", "my-deploy"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "kind", "Deployment"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "namespace", "default"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "current_stream_name", "default"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "selected", "true"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "otel_service_name", "my-deploy-default"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "data_stream_names.#", "1"),
					resource.TestCheckResourceAttr("odigos-central_source.example", "data_stream_names.0", "default"),
				),
			},
		},
	})
}

func TestAccOdigosSourceResource_ClusterNotFound(t *testing.T) {
	server, hostname := startMockOdigosServerForSource(t, "[]")
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccOdigosSourceResourceConfig(hostname, "TestCluster", "my-deploy", "Deployment", "default", "default"),
				ExpectError: mustRegex("not found|Error Resolving Cluster"),
			},
		},
	})
}

// TestAccOdigosSourceResource_UnsupportedClusterIsSoftFail verifies that a
// cluster running an unsupported Odigos version does not crash the apply but
// surfaces a warning and a clear error explaining nothing was created.
func TestAccOdigosSourceResource_UnsupportedClusterIsSoftFail(t *testing.T) {
	server, hostname := startMockOdigosServerForSource(t, onePlatformForSourceTooOld)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccOdigosSourceResourceConfig(hostname, "TestCluster", "my-deploy", "Deployment", "default", "default"),
				ExpectError: mustRegex("Cannot Create Source on Unsupported Cluster|not supported"),
			},
		},
	})
}

func testAccOdigosSourceResourceConfig(hostname, clusterName, name, kind, namespace, currentStreamName string) string {
	return `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure_skip_verify = true
}

resource "odigos-central_source" "example" {
  cluster_name       = "` + clusterName + `"
  name               = "` + name + `"
  kind               = "` + kind + `"
  namespace          = "` + namespace + `"
  current_stream_name = "` + currentStreamName + `"
}
`
}
