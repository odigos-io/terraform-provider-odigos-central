package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// startMockOdigosServerForDataSource starts an httptest.Server that mimics the Odigos GraphQL API.
// computePlatformsResponse must be the JSON array for "computePlatforms" (e.g. "[]" or "[{...}]").
// Returns the server and the hostname to use in the provider (host:port, no scheme).
func startMockOdigosServerForDataSource(t *testing.T, computePlatformsResponse string) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Query string `json:"query"`
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
			// Wrap the provided array in the full response structure
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + computePlatformsResponse + `},"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))
	hostname := strings.TrimPrefix(server.URL, "http://")
	return server, hostname
}

// TestAccOdigosComputePlatformsDataSource_Empty verifies that when the API returns
// zero compute platforms, the data source state has compute_platforms.# = 0. It does
// not test "data source type missing" (that would be a provider registration concern).
func TestAccOdigosComputePlatformsDataSource_Empty(t *testing.T) {
	server, hostname := startMockOdigosServerForDataSource(t, "[]")
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOdigosComputePlatformsDataSourceConfig(hostname),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.#", "0"),
				),
			},
		},
	})
}

// onePlatformResponse is the raw JSON array of one compute platform for the mock API.
const onePlatformResponse = `[{"id":"cluster-1","name":"TestCluster","odigosVersion":"1.2.3","type":"kubernetes","status":"connected","connectedAt":1700000000,"lastSeenAt":1700000100,"users":["u1@example.com"],"teams":["team-a"],"__typename":"ComputePlatform"}]`

func TestAccOdigosComputePlatformsDataSource_OnePlatform(t *testing.T) {
	server, hostname := startMockOdigosServerForDataSource(t, onePlatformResponse)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccOdigosComputePlatformsDataSourceConfig(hostname),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.#", "1"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.id", "cluster-1"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.name", "TestCluster"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.odigos_version", "1.2.3"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.type", "kubernetes"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.status", "connected"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.connected_at", "1700000000"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.last_seen_at", "1700000100"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.type_name", "ComputePlatform"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.users.#", "1"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.users.0", "u1@example.com"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.teams.#", "1"),
					resource.TestCheckResourceAttr("data.odigos_compute_platforms.example", "compute_platforms.0.teams.0", "team-a"),
				),
			},
		},
	})
}

func TestAccOdigosComputePlatformsDataSource_ApiError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		query := strings.TrimSpace(body.Query)
		if strings.Contains(query, "signIn") || strings.Contains(query, "SignIn") {
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
			return
		}
		if strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms") {
			// Simulate API error for compute platforms query
			_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"internal server error"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
	}))
	defer server.Close()
	hostname := strings.TrimPrefix(server.URL, "http://")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      testAccOdigosComputePlatformsDataSourceConfig(hostname),
				ExpectError: mustRegex("Unable to Get Compute Platforms|compute platforms query failed|internal server error"),
			},
		},
	})
}

func testAccOdigosComputePlatformsDataSourceConfig(hostname string) string {
	return `provider "odigos" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure = true
}

data "odigos_compute_platforms" "example" {}
`
}
