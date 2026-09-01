// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

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
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccProtoV6ProviderFactories is used to instantiate a provider during acceptance testing.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"odigos-central": providerserver.NewProtocol6WithError(New("test")()),
}

// startMockOdigosServer starts an httptest TLS server that mimics the Odigos
// GraphQL API well enough to satisfy the provider's Configure-time handshake:
//   - SignIn returns a valid access token.
//   - GetSystemConfig reports a Central version >= MinCentralVersion.
//   - GetComputePlatforms returns an empty list (no clusters).
//
// The server speaks HTTPS with a self-signed certificate because the provider
// always uses HTTPS, so test configs must set insecure_skip_verify = true.
//
// Returns the server and the hostname (host:port, no scheme) for the provider block.
func startMockOdigosServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		case strings.Contains(query, "systemConfig") || strings.Contains(query, "GetSystemConfig"):
			_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.22.0"}},"errors":[]}`))
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":[]},"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))
	hostname := strings.TrimPrefix(server.URL, "https://")
	return server, hostname
}

func TestAccProviderNoHostname(t *testing.T) {
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  username = "user"
  password = "secret"
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
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
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "user"
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
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
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  password = "secret"
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
				ExpectError: mustRegex("Missing Odigos API Username|username"),
			},
		},
	})
}

func TestAccProviderUsernamePassword(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
			},
		},
	})
}

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
				Config: `provider "odigos-central" {
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
			},
		},
	})
}

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
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
			},
		},
	})
}

// TestAccProviderRejectsOldCentral verifies the version-handshake hard fail
// surfaces a clear "upgrade Central" diagnostic.
func TestAccProviderRejectsOldCentral(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		q := strings.TrimSpace(body.Query)
		switch {
		case strings.Contains(q, "SignIn") || strings.Contains(q, "signIn"):
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{}}},"errors":[]}`))
		case strings.Contains(q, "systemConfig") || strings.Contains(q, "GetSystemConfig"):
			_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.18.0"}},"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))
	defer server.Close()
	hostname := strings.TrimPrefix(server.URL, "https://")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "u"
  password = "p"
  insecure_skip_verify = true
}
data "odigos-central_compute_platforms" "_" {}`,
				ExpectError: mustRegex("Odigos Central Version Not Supported|requires Odigos Central"),
			},
		},
	})
}

// TestAccProviderRejectsInsecureArgument verifies the plain-HTTP escape hatch is
// not reachable from configuration. The provider must never downgrade to HTTP,
// so "insecure" is not a valid argument and Terraform should reject the config.
func TestAccProviderRejectsInsecureArgument(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
  insecure = true
}
data "odigos-central_compute_platforms" "_" {}`,
				ExpectError: mustRegex(`An argument named "insecure" is not expected here|Unsupported argument`),
			},
		},
	})
}

// TestAccProviderVerifiesTLSByDefault asserts that omitting insecure_skip_verify
// leaves certificate verification on: the mock server's self-signed certificate
// must be rejected rather than silently trusted.
func TestAccProviderVerifiesTLSByDefault(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
}
data "odigos-central_compute_platforms" "_" {}`,
				ExpectError: mustRegex("certificate|x509|tls"),
			},
		},
	})
}

// TestAccProviderInsecureSkipVerifyFromEnv verifies ODIGOS_INSECURE_SKIP_VERIFY
// is honored when the attribute is absent from the configuration.
func TestAccProviderInsecureSkipVerifyFromEnv(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	t.Setenv("ODIGOS_INSECURE_SKIP_VERIFY", "true")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
}
data "odigos-central_compute_platforms" "_" {}`,
			},
		},
	})
}

// TestAccProviderInsecureSkipVerifyInvalidEnv verifies a non-boolean
// ODIGOS_INSECURE_SKIP_VERIFY produces a clear diagnostic rather than being
// silently ignored.
func TestAccProviderInsecureSkipVerifyInvalidEnv(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	t.Setenv("ODIGOS_INSECURE_SKIP_VERIFY", "yes-please")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `provider "odigos-central" {
  hostname = "` + hostname + `"
  username = "testuser"
  password = "testpass"
}
data "odigos-central_compute_platforms" "_" {}`,
				ExpectError: mustRegex("Invalid ODIGOS_INSECURE_SKIP_VERIFY Value"),
			},
		},
	})
}

// mustRegex returns a regexp.Regexp for use in ExpectError; it panics on invalid pattern.
func mustRegex(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}
