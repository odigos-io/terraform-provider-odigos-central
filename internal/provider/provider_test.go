package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccProtoV6ProviderFactories is used to instantiate a provider during acceptance testing.
// The factory function is called for each Terraform CLI command to create a provider
// server that the CLI can connect to and interact with.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"odigos": providerserver.NewProtocol6WithError(New("test")()),
}

// testAccProtoV6ProviderFactoriesWithEcho includes the echo provider alongside the odigos provider.
// It allows for testing assertions on data returned by an ephemeral resource during Open.
// The echoprovider is used to arrange tests by echoing ephemeral data into the Terraform state.
// This lets the data be referenced in test assertions with state checks.
var testAccProtoV6ProviderFactoriesWithEcho = map[string]func() (tfprotov6.ProviderServer, error){
	"odigos": providerserver.NewProtocol6WithError(New("test")()),
	"echo":   echoprovider.NewProviderServer(),
}

func testAccPreCheck(t *testing.T) {
	// You can add code here to run prior to any test case execution, for example assertions
	// about the appropriate environment variables being set are common to see in a pre-check
	// function.
}

// startMockOdigosServer starts an httptest.Server that mimics the Odigos GraphQL API:
// - SignIn mutation returns a valid access token.
// - GetComputePlatforms query returns an empty list.
// Returns the server and the hostname to use in the provider (host:port, no scheme).
func startMockOdigosServer(t *testing.T) (*httptest.Server, string) {
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
			// Auth response
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			// GetComputePlatforms response
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":[]},"errors":[]}`))
		default:
			// Generic success for other operations
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))
	// Hostname for provider: "host:port" without scheme (client adds http:// when insecure)
	hostname := strings.TrimPrefix(server.URL, "http://")
	return server, hostname
}

func TestAccProviderNoHostname(t *testing.T) {
	// Assumes ODIGOS_* env vars are not set; provider has no hostname and fails with "Missing hostname".
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Data source only to trigger provider Configure; we assert only on provider error.
				Config: `provider "odigos" {
  username = "user"
  password = "secret"
  insecure = true
}
data "odigos_compute_platforms" "_" {}`,
				ExpectError: mustRegex("Missing Odigos API Hostname|hostname"),
			},
		},
	})
}

func TestAccProviderMissingPassword(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos" {
  hostname = "` + hostname + `"
  username = "user"
  insecure = true
}
data "odigos_compute_platforms" "_" {}`,
				ExpectError: mustRegex("Missing Odigos API Password|password"),
			},
		},
	})
}

func TestAccProviderMissingUsername(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos" {
  hostname = "` + hostname + `"
  password = "secret"
  insecure = true
}
data "odigos_compute_platforms" "_" {}`,
				ExpectError: mustRegex("Missing Odigos API Username|username"),
			},
		},
	})
}

// Success-case tests below have no Check: they pass only if the step completes
// without error. That implies the provider Configure ran, the API client was
// created, and auth with the mock server succeeded. If any of that failed,
// Terraform would error and the test would fail.

func TestAccProviderUsernamePassword(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure = true
}
data "odigos_compute_platforms" "_" {}`,
				// Asserts success by step not erroring (provider config + client auth succeed).
			},
		},
	})
}

// TestAccProviderConfigFromEnv verifies the provider uses ODIGOS_* env vars when
// the provider block omits hostname/username/password. Asserts success by step not erroring.
func TestAccProviderConfigFromEnv(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	t.Setenv("ODIGOS_HOSTNAME", hostname)
	t.Setenv("ODIGOS_USERNAME", "envuser")
	t.Setenv("ODIGOS_PASSWORD", "envpass")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos" {
  insecure = true
}
data "odigos_compute_platforms" "_" {}`,
			},
		},
	})
}

// TestAccProviderConfigOverridesEnv verifies config block values override env vars
// (wrong hostname in env; correct hostname in config). Asserts success by step not erroring.
func TestAccProviderConfigOverridesEnv(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	t.Setenv("ODIGOS_HOSTNAME", "wrong.example.com")
	t.Setenv("ODIGOS_USERNAME", "envuser")
	t.Setenv("ODIGOS_PASSWORD", "envpass")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure = true
}
data "odigos_compute_platforms" "_" {}`,
			},
		},
	})
}

// mustRegex returns a regexp.Regexp for use in ExpectError; it panics on invalid pattern.
func mustRegex(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
