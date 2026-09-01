// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rsschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func emptyState(t *testing.T, schema rsschema.Schema) tfsdk.State {
	t.Helper()
	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected an object schema")
	}
	attrs := make(map[string]tftypes.Value, len(objType.AttributeTypes))
	for name, typ := range objType.AttributeTypes {
		attrs[name] = tftypes.NewValue(typ, nil)
	}
	return tfsdk.State{Schema: schema, Raw: tftypes.NewValue(objType, attrs)}
}

func TestParseSlashID(t *testing.T) {
	parts, diags := parseSlashID("cluster/ns/default", 3, "cluster_name/namespace/current_stream_name")
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if got, want := parts, []string{"cluster", "ns", "default"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("parts = %v, want %v", got, want)
	}

	_, diags = parseSlashID("cluster/ns", 3, "cluster_name/namespace/current_stream_name")
	if !diags.HasError() {
		t.Error("expected an error for the wrong number of segments")
	}

	_, diags = parseSlashID("cluster//default", 3, "cluster_name/namespace/current_stream_name")
	if !diags.HasError() {
		t.Error("expected an error for an empty segment")
	}
}

func TestNamespaceImportState(t *testing.T) {
	r := &namespaceResource{}
	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)

	resp := &fwresource.ImportStateResponse{State: emptyState(t, schemaResp.Schema)}
	r.ImportState(context.Background(), fwresource.ImportStateRequest{
		ID: "TestCluster/default/alpha",
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}

	var stored NamespaceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if stored.ClusterName.ValueString() != "TestCluster" || stored.Name.ValueString() != "default" || stored.CurrentStreamName.ValueString() != "alpha" {
		t.Errorf("imported state = cluster=%q name=%q stream=%q", stored.ClusterName.ValueString(), stored.Name.ValueString(), stored.CurrentStreamName.ValueString())
	}
}

func TestSourceImportState(t *testing.T) {
	r := &sourceResource{}
	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)

	resp := &fwresource.ImportStateResponse{State: emptyState(t, schemaResp.Schema)}
	r.ImportState(context.Background(), fwresource.ImportStateRequest{
		ID: "TestCluster/default/Deployment/my-deploy/alpha",
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected diagnostics: %v", resp.Diagnostics)
	}

	var stored SourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if stored.ClusterName.ValueString() != "TestCluster" ||
		stored.Namespace.ValueString() != "default" ||
		stored.Kind.ValueString() != "Deployment" ||
		stored.Name.ValueString() != "my-deploy" ||
		stored.CurrentStreamName.ValueString() != "alpha" {
		t.Errorf("imported state = %+v", stored)
	}
}

func TestNamespaceImportStateRejectsBadID(t *testing.T) {
	r := &namespaceResource{}
	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)

	resp := &fwresource.ImportStateResponse{State: emptyState(t, schemaResp.Schema)}
	r.ImportState(context.Background(), fwresource.ImportStateRequest{ID: "only-one-part"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for a malformed import ID")
	}
}
