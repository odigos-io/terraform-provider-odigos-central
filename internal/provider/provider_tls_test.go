// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"

	"github.com/odigos-io/odigos-central-client/central"
)

// TestProviderSchemaOmitsInsecure guards the security decision that the
// plain-HTTP downgrade is not reachable from configuration: exposing it would
// let a practitioner send the Central password over the wire in cleartext.
func TestProviderSchemaOmitsInsecure(t *testing.T) {
	resp := providerSchema(t)

	if _, found := resp.Schema.Attributes["insecure"]; found {
		t.Error(`provider schema exposes "insecure"; the plain-HTTP downgrade must not be configurable`)
	}
}

// TestProviderSchemaInsecureSkipVerify checks the TLS-verification escape hatch
// is present, optional, and boolean.
func TestProviderSchemaInsecureSkipVerify(t *testing.T) {
	resp := providerSchema(t)

	attr, found := resp.Schema.Attributes["insecure_skip_verify"]
	if !found {
		t.Fatal(`provider schema is missing "insecure_skip_verify"`)
	}
	if _, ok := attr.(schema.BoolAttribute); !ok {
		t.Errorf("insecure_skip_verify should be a BoolAttribute, got %T", attr)
	}
	if !attr.IsOptional() {
		t.Error("insecure_skip_verify should be optional")
	}
	if attr.IsRequired() {
		t.Error("insecure_skip_verify should not be required")
	}
}

func TestProviderSchemaHasMarkdownDescription(t *testing.T) {
	resp := providerSchema(t)
	if strings.TrimSpace(resp.Schema.MarkdownDescription) == "" && strings.TrimSpace(resp.Schema.Description) == "" {
		t.Error("provider schema is missing MarkdownDescription; generated registry docs would be blank")
	}
}

func providerSchema(t *testing.T) provider.SchemaResponse {
	t.Helper()
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("provider schema returned error diagnostics: %v", resp.Diagnostics)
	}
	return resp
}

// TestClientTLSVerificationEnforcedByDefault confirms that the configuration
// the provider builds rejects an untrusted certificate unless the practitioner
// opts out. startMockOdigosServer serves HTTPS with a self-signed certificate,
// which stands in for a Central server without a trusted chain.
func TestClientTLSVerificationEnforcedByDefault(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	_, err := central.NewClient(context.Background(), central.ClientConfig{
		Hostname: hostname,
		Username: "testuser",
		Password: "testpass",
	})
	if err == nil {
		t.Fatal("expected the self-signed certificate to be rejected, got a usable client")
	}

	var certErr *tls.CertificateVerificationError
	if !errors.As(err, &certErr) {
		t.Errorf("expected a TLS certificate verification error, got: %v", err)
	}
}

// TestClientInsecureSkipVerifyAcceptsSelfSigned confirms insecure_skip_verify
// actually reaches the TLS layer, so the option does what it advertises.
func TestClientInsecureSkipVerifyAcceptsSelfSigned(t *testing.T) {
	server, hostname := startMockOdigosServer(t)
	defer server.Close()

	client, err := central.NewClient(context.Background(), central.ClientConfig{
		Hostname:           hostname,
		Username:           "testuser",
		Password:           "testpass",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("expected the self-signed certificate to be accepted, got: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client, got nil")
	}
}
