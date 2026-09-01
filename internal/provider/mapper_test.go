// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/types"

	odigostypes "github.com/odigos-io/odigos-central-client/types"
)

// TestNamespaceDetailToModelEmptySlicesAreEmptyLists pins the representation of
// "no data streams" / "no sources" to an empty list, matching mapSourceToModel.
func TestNamespaceDetailToModelEmptySlicesAreEmptyLists(t *testing.T) {
	model, diags := namespaceDetailToModel(
		context.Background(),
		types.StringValue("TestCluster"),
		types.StringValue("default"),
		odigostypes.K8sActualNamespaceDetail{
			Name:     "default",
			Selected: true,
		},
	)
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if model.DataStreamNames.IsNull() {
		t.Error("data_stream_names is null; a missing API slice must become an empty list")
	}
	if got := len(model.DataStreamNames.Elements()); got != 0 {
		t.Errorf("data_stream_names has %d elements, want 0", got)
	}
	if model.Sources.IsNull() {
		t.Error("sources is null; a missing API slice must become an empty list")
	}
	if got := len(model.Sources.Elements()); got != 0 {
		t.Errorf("sources has %d elements, want 0", got)
	}
}

func TestMapSourceToModelMapsContainersAndConditions(t *testing.T) {
	transition := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	model := SourceResourceModel{}

	diags := mapSourceToModel(&model, "cluster-1", odigostypes.Source{
		Name:            "my-deploy",
		Namespace:       "default",
		Kind:            "Deployment",
		OtelServiceName: "my-deploy-default",
		Selected:        true,
		DataStreamNames: []string{"default"},
		Containers: []odigostypes.SourceContainer{{
			ContainerName:          "app",
			Language:               "go",
			RuntimeVersion:         "1.25.5",
			Instrumented:           true,
			InstrumentationMessage: "ok",
			OtelDistroName:         "golang",
		}},
		Conditions: []odigostypes.SourceCondition{{
			Status:             "True",
			Type:               "Ready",
			Reason:             "Instrumented",
			Message:            "running",
			LastTransitionTime: &transition,
		}},
	})
	if diags.HasError() {
		t.Fatalf("unexpected diagnostics: %v", diags)
	}
	if model.ClusterID.ValueString() != "cluster-1" {
		t.Errorf("cluster_id = %q, want cluster-1", model.ClusterID.ValueString())
	}
	if model.OtelServiceName.ValueString() != "my-deploy-default" {
		t.Errorf("otel_service_name = %q", model.OtelServiceName.ValueString())
	}
	if !model.Selected.ValueBool() {
		t.Error("selected = false, want true")
	}
	if got := len(model.Containers.Elements()); got != 1 {
		t.Fatalf("containers.# = %d, want 1", got)
	}
	if got := len(model.Conditions.Elements()); got != 1 {
		t.Fatalf("conditions.# = %d, want 1", got)
	}
}
