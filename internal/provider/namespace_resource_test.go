// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// startMockOdigosServerForNamespace starts an httptest.Server that supports the namespace resource:
// signIn, GetComputePlatforms (one platform so getClusterID works), and RemoteFetch for
// GetNamespace and PersistNamespaceSources. computePlatformsJSON is the array for computePlatforms.
func startMockOdigosServerForNamespace(t *testing.T, computePlatformsJSON string) (*httptest.Server, string) {
	t.Helper()

	// Inner response for GetNamespace: the client now uses the list-with-sources
	// query (GET_NAMESPACES_WITH_SOURCES) and selects the namespace by name
	// client-side, so the payload is the k8sActualNamespaces array.
	// sources: array of {namespace, kind, name, dataStreamNames, selected, numberOfInstances}
	innerNamespaceResponse := `{"type":"","data":{"computePlatform":{"k8sActualNamespaces":[{"name":"default","selected":true,"dataStreamNames":["default"],"sources":[{"namespace":"default","kind":"Deployment","name":"my-app","dataStreamNames":["default"],"selected":true,"numberOfInstances":1}]}]}},"request_id":""}`

	// Inner response for PersistNamespaceSources.
	innerPersistNamespacesResponse := `{"type":"","data":{"persistK8sNamespaces":true,"errors":[]},"request_id":""}`

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
			if strings.Contains(innerQuery, "k8sActualNamespaces") || strings.Contains(innerQuery, "GetNamespace") {
				inner = innerNamespaceResponse
			} else if strings.Contains(innerQuery, "persistK8sNamespaces") {
				inner = innerPersistNamespacesResponse
			} else {
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

const onePlatformForNamespace = `[{"id":"cluster-1","name":"TestCluster","odigosVersion":"v1.22.0","type":"k8s","status":"connected","connectedAt":0,"lastSeenAt":0,"users":[],"teams":[],"__typename":"ComputePlatform"}]`

func TestAccOdigosNamespaceResource_CreateReadDelete(t *testing.T) {
	server, hostname := startMockOdigosServerForNamespace(t, onePlatformForNamespace)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOdigosNamespaceResourceConfig(hostname, "TestCluster", "default", "default"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "cluster_name", "TestCluster"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "name", "default"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "current_stream_name", "default"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "selected", "true"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "data_stream_names.#", "1"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "data_stream_names.0", "default"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "sources.#", "1"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "sources.0.namespace", "default"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "sources.0.kind", "Deployment"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "sources.0.name", "my-app"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "sources.0.selected", "true"),
					resource.TestCheckResourceAttr("odigos-central_namespace.example", "sources.0.number_of_instances", "1"),
				),
			},
		},
	})
}

func TestAccOdigosNamespaceResource_ClusterNotFound(t *testing.T) {
	server, hostname := startMockOdigosServerForNamespace(t, "[]")
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccOdigosNamespaceResourceConfig(hostname, "TestCluster", "default", "default"),
				ExpectError: mustRegex("not found|Error Resolving Cluster"),
			},
		},
	})
}

func testAccOdigosNamespaceResourceConfig(hostname, clusterName, namespaceName, currentStreamName string) string {
	return `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure_skip_verify = true
}

resource "odigos-central_namespace" "example" {
  cluster_name        = "` + clusterName + `"
  name                = "` + namespaceName + `"
  current_stream_name = "` + currentStreamName + `"
}
`
}
