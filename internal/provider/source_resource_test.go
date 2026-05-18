package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// startMockOdigosServerForSource starts an httptest.Server that supports the source resource:
// signIn, GetComputePlatforms (one platform so getClusterID works), and RemoteFetch for
// GetSource and PersistSources. computePlatformsJSON is the array for computePlatforms (e.g. one platform with id and name).
// Returns server and hostname for the provider.
func startMockOdigosServerForSource(t *testing.T, computePlatformsJSON string) (*httptest.Server, string) {
	t.Helper()

	// Inner response for GetSource: sourceResponse shape (type, data.computePlatform.source, request_id).
	innerSourceResponse := func(namespace, name, kind, otelServiceName string, dataStreamNames []string) string {
		streams, _ := json.Marshal(dataStreamNames)
		return `{"type":"","data":{"computePlatform":{"source":{"namespace":"` + namespace + `","name":"` + name + `","kind":"` + kind + `","dataStreamNames":` + string(streams) + `,"selected":true,"otelServiceName":"` + otelServiceName + `","containers":[],"conditions":[]}}},"request_id":""}`
	}

	// Inner response for PersistSources.
	innerPersistResponse := `{"type":"","data":{"persistK8sSources":true},"request_id":""}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + computePlatformsJSON + `},"errors":[]}`))
		case strings.Contains(query, "remoteFetch") || strings.Contains(query, "RemoteFetch"):
			innerQuery, _ := body.Variables["query"].(string)
			var inner string
			if strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource") {
				// Return a source matching typical create (namespace, name, kind from variables if present)
				inner = innerSourceResponse("default", "my-deploy", "Deployment", "my-deploy-default", []string{"default"})
			} else if strings.Contains(innerQuery, "persistK8sSources") {
				inner = innerPersistResponse
			} else {
				inner = `{"type":"","data":{},"request_id":""}`
			}
			remoteFetchVal, _ := json.Marshal(inner)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(remoteFetchVal) + `},"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))
	hostname := strings.TrimPrefix(server.URL, "http://")
	return server, hostname
}

// onePlatformForSource is a single compute platform so getClusterID("TestCluster") returns "cluster-1".
const onePlatformForSource = `[{"id":"cluster-1","name":"TestCluster","odigosVersion":"1.0","type":"kubernetes","status":"connected","connectedAt":0,"lastSeenAt":0,"users":[],"teams":[],"__typename":"ComputePlatform"}]`

func TestAccOdigosSourceResource_CreateReadDelete(t *testing.T) {
	server, hostname := startMockOdigosServerForSource(t, onePlatformForSource)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOdigosSourceResourceConfig(hostname, "TestCluster", "my-deploy", "Deployment", "default", "default"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("odigos_source.example", "cluster_name", "TestCluster"),
					resource.TestCheckResourceAttr("odigos_source.example", "cluster_id", "cluster-1"),
					resource.TestCheckResourceAttr("odigos_source.example", "name", "my-deploy"),
					resource.TestCheckResourceAttr("odigos_source.example", "kind", "Deployment"),
					resource.TestCheckResourceAttr("odigos_source.example", "namespace", "default"),
					resource.TestCheckResourceAttr("odigos_source.example", "current_stream_name", "default"),
					resource.TestCheckResourceAttr("odigos_source.example", "otel_service_name", "my-deploy-default"),
					resource.TestCheckResourceAttr("odigos_source.example", "data_stream_names.#", "1"),
					resource.TestCheckResourceAttr("odigos_source.example", "data_stream_names.0", "default"),
				),
			},
		},
	})
}

func TestAccOdigosSourceResource_ClusterNotFound(t *testing.T) {
	// No platforms so getClusterID("TestCluster") will fail.
	server, hostname := startMockOdigosServerForSource(t, "[]")
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccOdigosSourceResourceConfig(hostname, "TestCluster", "my-deploy", "Deployment", "default", "default"),
				ExpectError: mustRegex("cluster.*not found|Error getting cluster ID"),
			},
		},
	})
}

func testAccOdigosSourceResourceConfig(hostname, clusterName, name, kind, namespace, currentStreamName string) string {
	return `provider "odigos" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure = true
}

resource "odigos_source" "example" {
  cluster_name       = "` + clusterName + `"
  name               = "` + name + `"
  kind               = "` + kind + `"
  namespace          = "` + namespace + `"
  current_stream_name = "` + currentStreamName + `"
}
`
}
