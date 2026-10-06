// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rsschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/odigos-io/odigos-central-client/central"
	odigostypes "github.com/odigos-io/odigos-central-client/types"
)

func newSourceResourceForTest(t *testing.T, hostname string) (*sourceResource, rsschema.Schema) {
	t.Helper()

	client, err := central.NewClient(context.Background(), central.ClientConfig{
		Hostname:           hostname,
		Username:           "testuser",
		Password:           "testpass",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("build central client: %v", err)
	}

	r := &sourceResource{client: &cachedOdigosClient{client: client}}
	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", schemaResp.Diagnostics)
	}
	return r, schemaResp.Schema
}

func sourceRawValue(t *testing.T, schema rsschema.Schema) tftypes.Value {
	t.Helper()

	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected the source schema to be an object type")
	}

	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"cluster_name":        tftypes.NewValue(tftypes.String, "TestCluster"),
		"name":                tftypes.NewValue(tftypes.String, "my-deploy"),
		"kind":                tftypes.NewValue(tftypes.String, "Deployment"),
		"namespace":           tftypes.NewValue(tftypes.String, "default"),
		"current_stream_name": tftypes.NewValue(tftypes.String, "default"),
		"selected":            tftypes.NewValue(tftypes.Bool, true),
		"data_stream_names":   tftypes.NewValue(objType.AttributeTypes["data_stream_names"], nil),
		"otel_service_name":   tftypes.NewValue(tftypes.String, nil),
		"cluster_id":          tftypes.NewValue(tftypes.String, nil),
		"containers":          tftypes.NewValue(objType.AttributeTypes["containers"], nil),
		"conditions":          tftypes.NewValue(objType.AttributeTypes["conditions"], nil),
	})
}

func sourceRawValueWithClusterID(t *testing.T, schema rsschema.Schema, clusterID string) tftypes.Value {
	t.Helper()

	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected the source schema to be an object type")
	}

	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"cluster_name":        tftypes.NewValue(tftypes.String, "TestCluster"),
		"name":                tftypes.NewValue(tftypes.String, "my-deploy"),
		"kind":                tftypes.NewValue(tftypes.String, "Deployment"),
		"namespace":           tftypes.NewValue(tftypes.String, "default"),
		"current_stream_name": tftypes.NewValue(tftypes.String, "default"),
		"selected":            tftypes.NewValue(tftypes.Bool, true),
		"data_stream_names":   tftypes.NewValue(objType.AttributeTypes["data_stream_names"], nil),
		"otel_service_name":   tftypes.NewValue(tftypes.String, nil),
		"cluster_id":          tftypes.NewValue(tftypes.String, clusterID),
		"containers":          tftypes.NewValue(objType.AttributeTypes["containers"], nil),
		"conditions":          tftypes.NewValue(objType.AttributeTypes["conditions"], nil),
	})
}

func serveSourceHandshake(w http.ResponseWriter, query string) bool {
	switch {
	case strings.Contains(query, "signIn") || strings.Contains(query, "SignIn"):
		_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
		return true
	case strings.Contains(query, "systemConfig") || strings.Contains(query, "GetSystemConfig"):
		_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.22.0"}},"errors":[]}`))
		return true
	case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
		_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + onePlatformForSource + `},"errors":[]}`))
		return true
	}
	return false
}

func writeRemoteFetch(w http.ResponseWriter, inner string) {
	encoded, _ := json.Marshal(inner)
	_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
}

const emptySourceGetInner = `{"type":"","data":{"computePlatform":{"source":{"namespace":"","name":"","kind":"","dataStreamNames":[],"selected":false,"otelServiceName":"","containers":[],"conditions":[]}}},"request_id":""}`

const missingInstrumentationConfigInner = `{"type":"","data":{"errors":[{"message":"failed to get InstrumentationConfig: instrumentationconfigs.odigos.io \"deployment-pricing\" not found"}]},"request_id":""}`

const unexpectedGetSourceErrorInner = `{"type":"","data":{"errors":[{"message":"internal server error"}]},"request_id":""}`

// TestSourceCreateErrorsWhenPersistReturnsFalse covers C5: a success=false
// mutation must not be recorded as a successful create.
func TestSourceCreateErrorsWhenPersistReturnsFalse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if strings.Contains(innerQuery, "persistK8sSources") {
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":false},"request_id":""}`)
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValue(t, schema)},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when persistK8sSources returned false")
	}
	if !resp.State.Raw.IsNull() {
		t.Error("Create wrote state after a failed persist")
	}
}

// TestSourceDeleteErrorsWhenPersistReturnsFalse covers C5 on the delete path.
func TestSourceDeleteErrorsWhenPersistReturnsFalse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if strings.Contains(innerQuery, "persistK8sSources") {
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":false},"request_id":""}`)
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	prior := sourceRawValue(t, schema)
	resp := &fwresource.DeleteResponse{}
	r.Delete(context.Background(), fwresource.DeleteRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when persistK8sSources returned false on delete")
	}
}

// TestSourceCreateErrorsWhenReadBackFails covers C6: a failed GetSource after
// persist must not fabricate empty computed attributes as authoritative state.
func TestSourceCreateErrorsWhenReadBackFails(t *testing.T) {
	var persisted bool
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if strings.Contains(innerQuery, "persistK8sSources") {
			mu.Lock()
			persisted = true
			mu.Unlock()
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
			return
		}
		if strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource") {
			mu.Lock()
			done := persisted
			mu.Unlock()
			if !done {
				writeRemoteFetch(w, emptySourceGetInner)
				return
			}
			encoded, _ := json.Marshal(`null`)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValue(t, schema)},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when GetSource failed after create")
	}
	if !resp.State.Raw.IsNull() {
		var stored SourceResourceModel
		_ = resp.State.Get(context.Background(), &stored)
		if stored.OtelServiceName.ValueString() == "" && !stored.OtelServiceName.IsNull() {
			t.Error("Create fabricated otel_service_name = \"\" after a failed read-back")
		}
	}
}

// TestSourceReadKeepsResourceWhenClusterIDChanges covers a cluster that
// re-registered with a new Central id. The workload is still there, so Read
// must keep the resource and refresh cluster_id rather than treating it as gone.
func TestSourceReadKeepsResourceWhenClusterIDChanges(t *testing.T) {
	server, hostname := startSourceGetMock(t, true, []string{"default"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValueWithClusterID(t, schema, "old-cluster-id")
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Read dropped the source after a cluster_id change; the workload still exists")
	}

	var stored SourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if got := stored.ClusterID.ValueString(); got != "cluster-1" {
		t.Errorf("cluster_id = %q, want the current proxy id %q", got, "cluster-1")
	}
	if resp.Diagnostics.WarningsCount() < 1 {
		t.Error("expected a warning that the cluster id changed")
	}
}

func sourceRawValueWithStream(t *testing.T, schema rsschema.Schema, stream string) tftypes.Value {
	t.Helper()

	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected the source schema to be an object type")
	}

	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"cluster_name":        tftypes.NewValue(tftypes.String, "TestCluster"),
		"name":                tftypes.NewValue(tftypes.String, "my-deploy"),
		"kind":                tftypes.NewValue(tftypes.String, "Deployment"),
		"namespace":           tftypes.NewValue(tftypes.String, "default"),
		"current_stream_name": tftypes.NewValue(tftypes.String, stream),
		"selected":            tftypes.NewValue(tftypes.Bool, true),
		"data_stream_names":   tftypes.NewValue(objType.AttributeTypes["data_stream_names"], nil),
		"otel_service_name":   tftypes.NewValue(tftypes.String, nil),
		"cluster_id":          tftypes.NewValue(tftypes.String, "cluster-1"),
		"containers":          tftypes.NewValue(objType.AttributeTypes["containers"], nil),
		"conditions":          tftypes.NewValue(objType.AttributeTypes["conditions"], nil),
	})
}

func sourceRawValueWithSelected(t *testing.T, schema rsschema.Schema, selected bool) tftypes.Value {
	t.Helper()

	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected the source schema to be an object type")
	}

	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"cluster_name":        tftypes.NewValue(tftypes.String, "TestCluster"),
		"name":                tftypes.NewValue(tftypes.String, "my-deploy"),
		"kind":                tftypes.NewValue(tftypes.String, "Deployment"),
		"namespace":           tftypes.NewValue(tftypes.String, "default"),
		"current_stream_name": tftypes.NewValue(tftypes.String, "default"),
		"selected":            tftypes.NewValue(tftypes.Bool, selected),
		"data_stream_names":   tftypes.NewValue(objType.AttributeTypes["data_stream_names"], nil),
		"otel_service_name":   tftypes.NewValue(tftypes.String, nil),
		"cluster_id":          tftypes.NewValue(tftypes.String, "cluster-1"),
		"containers":          tftypes.NewValue(objType.AttributeTypes["containers"], nil),
		"conditions":          tftypes.NewValue(objType.AttributeTypes["conditions"], nil),
	})
}

type sourcePersistCall struct {
	Name      string
	Namespace string
	Kind      string
	Stream    string
	Selected  bool
}

func startSourceGetMock(t *testing.T, selected bool, streams []string) (*httptest.Server, string) {
	t.Helper()
	streamsJSON, err := json.Marshal(streams)
	if err != nil {
		t.Fatalf("marshal streams: %v", err)
	}
	inner := `{"type":"","data":{"computePlatform":{"source":{"namespace":"default","name":"my-deploy","kind":"Deployment","dataStreamNames":` +
		string(streamsJSON) + `,"selected":` + boolJSON(selected) + `,"otelServiceName":"my-deploy-default","containers":[],"conditions":[]}}},"request_id":""}`

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource") {
			writeRemoteFetch(w, inner)
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
	}))
	return server, strings.TrimPrefix(server.URL, "https://")
}

func boolJSON(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func decodeSourcePersistCalls(t *testing.T, outerVars map[string]any) []sourcePersistCall {
	t.Helper()
	innerVars, ok := outerVars["variables"].(map[string]any)
	if !ok {
		t.Fatalf("remoteFetch variables missing inner variables: %#v", outerVars)
	}
	raw, err := json.Marshal(innerVars["sources"])
	if err != nil {
		t.Fatalf("marshal sources: %v", err)
	}
	var inputs []struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		Kind              string `json:"kind"`
		CurrentStreamName string `json:"currentStreamName"`
		Selected          bool   `json:"selected"`
	}
	if err := json.Unmarshal(raw, &inputs); err != nil {
		t.Fatalf("unmarshal sources: %v", err)
	}
	result := make([]sourcePersistCall, 0, len(inputs))
	for _, in := range inputs {
		result = append(result, sourcePersistCall{
			Name: in.Name, Namespace: in.Namespace, Kind: in.Kind, Stream: in.CurrentStreamName, Selected: in.Selected,
		})
	}
	return result
}

func startSourceStreamMoveMock(t *testing.T, sourceStreams []string) (*httptest.Server, string, *[]sourcePersistCall) {
	t.Helper()
	var mu sync.Mutex
	calls := make([]sourcePersistCall, 0, 2)
	streamsJSON, err := json.Marshal(sourceStreams)
	if err != nil {
		t.Fatalf("marshal streams: %v", err)
	}
	getInner := `{"type":"","data":{"computePlatform":{"source":{"namespace":"default","name":"my-deploy","kind":"Deployment","dataStreamNames":` +
		string(streamsJSON) + `,"selected":true,"otelServiceName":"my-deploy-default","containers":[],"conditions":[]}}},"request_id":""}`

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		switch {
		case strings.Contains(innerQuery, "persistK8sSources"):
			mu.Lock()
			calls = append(calls, decodeSourcePersistCalls(t, body.Variables)...)
			mu.Unlock()
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
		case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
			writeRemoteFetch(w, getInner)
		default:
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
		}
	}))
	return server, strings.TrimPrefix(server.URL, "https://"), &calls
}

func TestSourceReadRefreshesSelectedWhenUnselected(t *testing.T) {
	server, hostname := startSourceGetMock(t, false, []string{"default"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValue(t, schema)
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("deselection is not an error, got: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Read dropped the resource because selected is false; selected is configuration, not identity")
	}
	var stored SourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if stored.Selected.ValueBool() {
		t.Error("selected = true, want false from the API")
	}
}

func TestSourceReadKeepsWhenInstrumentationConfigMissing(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource") {
			writeRemoteFetch(w, missingInstrumentationConfigInner)
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	prior := sourceRawValue(t, schema)
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("missing InstrumentationConfig after unselect is not an error, got: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Read dropped the resource because the InstrumentationConfig was deleted; selected is configuration, not identity")
	}
	var stored SourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if stored.Selected.ValueBool() {
		t.Error("selected = true, want false when GetSource reports a missing InstrumentationConfig")
	}
}

func TestSourceReadKeepsWhenNotInStream(t *testing.T) {
	server, hostname := startSourceGetMock(t, true, []string{"beta"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValueWithStream(t, schema, "alpha")
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("leaving a stream is not an error, got: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("Read dropped the resource because it is not in the configured stream; that is drift, not deletion")
	}
}

func TestSourceReadKeepsWhenSelectedInStream(t *testing.T) {
	server, hostname := startSourceGetMock(t, true, []string{"alpha", "beta"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValueWithStream(t, schema, "alpha")
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("expected the resource to remain in state while the source is still selected on the configured stream")
	}
}

func TestSourceReadKeepsStateOnUnsupportedCluster(t *testing.T) {
	client := &cachedOdigosClient{}
	client.proxies.Store("TestCluster", (*central.ProxyClient)(nil))
	r := &sourceResource{client: client}

	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema

	prior := sourceRawValue(t, schema)
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an unsupported cluster is a soft failure on Read, got: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Error("Read removed the resource from state because the cluster is unsupported")
	}
	if resp.Diagnostics.WarningsCount() < 1 {
		t.Error("expected a warning that the cluster is unsupported")
	}
}

func TestSourceDeleteErrorsOnUnsupportedCluster(t *testing.T) {
	client := &cachedOdigosClient{}
	client.proxies.Store("TestCluster", (*central.ProxyClient)(nil))
	r := &sourceResource{client: client}

	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema

	prior := sourceRawValue(t, schema)
	resp := &fwresource.DeleteResponse{}
	r.Delete(context.Background(), fwresource.DeleteRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when deleting a source on an unsupported cluster")
	}
}

func TestSourceUpdateAddsNewStreamBeforeRemovingOld(t *testing.T) {
	server, hostname, calls := startSourceStreamMoveMock(t, []string{"beta"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValueWithStream(t, schema, "alpha")
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "beta")},
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}

	want := []sourcePersistCall{
		{Name: "my-deploy", Namespace: "default", Kind: "Deployment", Stream: "beta", Selected: true},
		{Name: "my-deploy", Namespace: "default", Kind: "Deployment", Stream: "alpha", Selected: false},
	}
	got := *calls
	if len(got) != len(want) {
		t.Fatalf("expected exactly 2 persist calls (add new, then remove old), got %d: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("persist call %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSourceUpdateKeepsPriorStreamWhenRemovalFails(t *testing.T) {
	var callCount int
	var mu sync.Mutex

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if !strings.Contains(innerQuery, "persistK8sSources") {
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
			return
		}
		mu.Lock()
		callCount++
		succeeded := callCount == 1
		mu.Unlock()
		if succeeded {
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":false},"request_id":""}`)
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	prior := sourceRawValueWithStream(t, schema, "alpha")
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "beta")},
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the source could not be removed from the old stream")
	}

	var stored SourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if got := stored.CurrentStreamName.ValueString(); got != "alpha" {
		t.Errorf("current_stream_name = %q, want the prior value %q so the next plan retries the move", got, "alpha")
	}
}

func TestSourceCreatePersistsSelectedFalse(t *testing.T) {
	server, hostname, calls := startSourceStreamMoveMock(t, []string{})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithSelected(t, schema, false)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	got := *calls
	if len(got) != 1 {
		t.Fatalf("expected 1 persist call, got %d: %+v", len(got), got)
	}
	if got[0].Selected {
		t.Errorf("persist selected = true, want false")
	}
}

func TestExistingSourceConflict(t *testing.T) {
	if msg := existingSourceConflict(odigostypes.Source{}, "defaultTwoStream"); msg != "" {
		t.Errorf("empty source: got %q, want empty", msg)
	}
	if msg := existingSourceConflict(odigostypes.Source{Name: "ads", Namespace: "ns", Kind: "Deployment"}, "defaultTwoStream"); msg != "" {
		t.Errorf("no streams: got %q, want empty", msg)
	}
	msg := existingSourceConflict(odigostypes.Source{
		Name: "inventory", Namespace: "kv-mall-1", Kind: "Deployment", DataStreamNames: []string{"default"},
	}, "defaultTwoStream")
	if !strings.Contains(msg, "Deployment/inventory") || !strings.Contains(msg, "data stream default") {
		t.Errorf("conflict message = %q", msg)
	}
	if strings.Contains(msg, "stream(s)") || strings.Contains(msg, "defaultTwoStream") {
		t.Errorf("conflict message listed extra streams: %q", msg)
	}
	msg = existingSourceConflict(odigostypes.Source{
		Name: "inventory", Namespace: "kv-mall-1", Kind: "Deployment",
		DataStreamNames: []string{"default", "defaultTwoStream"},
	}, "defaultTwoStream")
	if !strings.Contains(msg, "data stream default") || strings.Contains(msg, "defaultTwoStream") {
		t.Errorf("conflict message should name only the existing stream, got %q", msg)
	}
}

func TestSourceCreateRejectsExistingStreams(t *testing.T) {
	server, hostname, calls := startSourceStreamMoveMock(t, []string{"default"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "defaultTwoStream")},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the workload is already a source on another stream")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "already an Odigos source") {
		t.Errorf("diagnostics = %v", resp.Diagnostics)
	}
	if !strings.Contains(detail, "data stream default") || strings.Contains(detail, "defaultTwoStream") || strings.Contains(detail, "stream(s)") {
		t.Errorf("diagnostics should name only the existing stream, got %v", resp.Diagnostics)
	}
	if got := *calls; len(got) != 0 {
		t.Errorf("persist was called %d times, want 0", len(got))
	}
}

func TestSourceCreateWhenInstrumentationConfigMissing(t *testing.T) {
	var persistCount atomic.Int32
	populated := `{"type":"","data":{"computePlatform":{"source":{"namespace":"default","name":"my-deploy","kind":"Deployment","dataStreamNames":["default"],"selected":true,"otelServiceName":"my-deploy","containers":[],"conditions":[]}}},"request_id":""}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		switch {
		case strings.Contains(innerQuery, "persistK8sSources"):
			persistCount.Add(1)
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
		case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
			if persistCount.Load() == 0 {
				writeRemoteFetch(w, missingInstrumentationConfigInner)
				return
			}
			writeRemoteFetch(w, populated)
		default:
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
		}
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValue(t, schema)},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create should persist when GetSource reports a missing InstrumentationConfig, got %v", resp.Diagnostics)
	}
	if persistCount.Load() != 1 {
		t.Errorf("persist called %d times, want 1", persistCount.Load())
	}
}

func TestSourceCreateFailsOnUnexpectedGetSourceError(t *testing.T) {
	var persistCount atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		switch {
		case strings.Contains(innerQuery, "persistK8sSources"):
			persistCount.Add(1)
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
		case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
			writeRemoteFetch(w, unexpectedGetSourceErrorInner)
		default:
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
		}
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValue(t, schema)},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Create to fail on an unexpected GetSource error")
	}
	if persistCount.Load() != 0 {
		t.Errorf("persist called %d times, want 0", persistCount.Load())
	}
}

func TestSourceUpdateTogglesSelected(t *testing.T) {
	server, hostname, calls := startSourceStreamMoveMock(t, []string{})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValueWithSelected(t, schema, true)
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithSelected(t, schema, false)},
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	got := *calls
	if len(got) != 1 {
		t.Fatalf("expected 1 persist call to disable, got %d: %+v", len(got), got)
	}
	if got[0].Stream != "default" || got[0].Selected {
		t.Errorf("persist = %+v, want stream=default selected=false", got[0])
	}
}

func TestSourceUpdateUnselectedWhenInstrumentationConfigMissing(t *testing.T) {
	var persistCount atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		switch {
		case strings.Contains(innerQuery, "persistK8sSources"):
			persistCount.Add(1)
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
		case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
			writeRemoteFetch(w, missingInstrumentationConfigInner)
		default:
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
		}
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	prior := sourceRawValueWithSelected(t, schema, true)
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithSelected(t, schema, false)},
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Update should keep the resource when GetSource reports a missing InstrumentationConfig after unselect, got %v", resp.Diagnostics)
	}
	if persistCount.Load() != 1 {
		t.Errorf("persist called %d times, want 1", persistCount.Load())
	}
	var stored SourceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if stored.Selected.ValueBool() {
		t.Error("selected = true, want false after unselect deleted the InstrumentationConfig")
	}
}

func TestSourceUpdateSelectedStillFailsOnMissingInstrumentationConfig(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		switch {
		case strings.Contains(innerQuery, "persistK8sSources"):
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
		case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
			writeRemoteFetch(w, missingInstrumentationConfigInner)
		default:
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
		}
	}))
	defer server.Close()

	r, schema := newSourceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))
	prior := sourceRawValueWithSelected(t, schema, false)
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: prior}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithSelected(t, schema, true)},
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Update to fail when selected=true but GetSource reports a missing InstrumentationConfig")
	}
}

func sourceRawValueWithDataStreams(t *testing.T, schema rsschema.Schema, current string, streams []string) tftypes.Value {
	t.Helper()

	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected the source schema to be an object type")
	}

	elems := make([]tftypes.Value, len(streams))
	for i, s := range streams {
		elems[i] = tftypes.NewValue(tftypes.String, s)
	}

	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"cluster_name":        tftypes.NewValue(tftypes.String, "TestCluster"),
		"name":                tftypes.NewValue(tftypes.String, "my-deploy"),
		"kind":                tftypes.NewValue(tftypes.String, "Deployment"),
		"namespace":           tftypes.NewValue(tftypes.String, "default"),
		"current_stream_name": tftypes.NewValue(tftypes.String, current),
		"selected":            tftypes.NewValue(tftypes.Bool, true),
		"data_stream_names":   tftypes.NewValue(objType.AttributeTypes["data_stream_names"], elems),
		"otel_service_name":   tftypes.NewValue(tftypes.String, nil),
		"cluster_id":          tftypes.NewValue(tftypes.String, "cluster-1"),
		"containers":          tftypes.NewValue(objType.AttributeTypes["containers"], nil),
		"conditions":          tftypes.NewValue(objType.AttributeTypes["conditions"], nil),
	})
}

func TestSourceDeleteRemovesOnlyOwnedStream(t *testing.T) {
	server, hostname, calls := startSourceStreamMoveMock(t, []string{"alpha", "beta"})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValueWithDataStreams(t, schema, "alpha", []string{"alpha", "beta"})
	resp := &fwresource.DeleteResponse{}
	r.Delete(context.Background(), fwresource.DeleteRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	got := *calls
	if len(got) != 1 {
		t.Fatalf("expected persist of the owned stream only, got %d: %+v", len(got), got)
	}
	if got[0].Stream != "alpha" || got[0].Selected {
		t.Errorf("persist = %+v, want stream=alpha selected=false", got[0])
	}
}

func TestSourceDeleteFallsBackToCurrentStream(t *testing.T) {
	server, hostname, calls := startSourceStreamMoveMock(t, []string{})
	defer server.Close()

	r, schema := newSourceResourceForTest(t, hostname)
	prior := sourceRawValue(t, schema)
	resp := &fwresource.DeleteResponse{}
	r.Delete(context.Background(), fwresource.DeleteRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	got := *calls
	if len(got) != 1 {
		t.Fatalf("expected 1 persist call, got %d: %+v", len(got), got)
	}
	if got[0].Stream != "default" || got[0].Selected {
		t.Errorf("persist = %+v, want stream=default selected=false", got[0])
	}
}

func TestSourceDeleteSerializesSameWorkload(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		if strings.Contains(innerQuery, "persistK8sSources") {
			n := inFlight.Add(1)
			for {
				m := maxInFlight.Load()
				if n <= m || maxInFlight.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			inFlight.Add(-1)
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
			return
		}
		writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
	}))
	defer server.Close()

	client, err := central.NewClient(context.Background(), central.ClientConfig{
		Hostname:           strings.TrimPrefix(server.URL, "https://"),
		Username:           "testuser",
		Password:           "testpass",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("build central client: %v", err)
	}
	cached := &cachedOdigosClient{client: client}
	r1 := &sourceResource{client: cached}
	r2 := &sourceResource{client: cached}

	schemaResp := &fwresource.SchemaResponse{}
	r1.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		resp := &fwresource.DeleteResponse{}
		r1.Delete(context.Background(), fwresource.DeleteRequest{
			State: tfsdk.State{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "alpha")},
		}, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("alpha delete: %v", resp.Diagnostics)
		}
	}()
	go func() {
		defer wg.Done()
		resp := &fwresource.DeleteResponse{}
		r2.Delete(context.Background(), fwresource.DeleteRequest{
			State: tfsdk.State{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "beta")},
		}, resp)
		if resp.Diagnostics.HasError() {
			t.Errorf("beta delete: %v", resp.Diagnostics)
		}
	}()
	wg.Wait()
	if maxInFlight.Load() != 1 {
		t.Errorf("maxInFlight concurrent persist = %d, want 1", maxInFlight.Load())
	}
}

func TestSourceCreateSerializesSameWorkload(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	var persistCount atomic.Int32
	populated := `{"type":"","data":{"computePlatform":{"source":{"namespace":"default","name":"my-deploy","kind":"Deployment","dataStreamNames":["alpha"],"selected":true,"otelServiceName":"my-deploy","containers":[],"conditions":[]}}},"request_id":""}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if serveSourceHandshake(w, strings.TrimSpace(body.Query)) {
			return
		}
		innerQuery, _ := body.Variables["query"].(string)
		switch {
		case strings.Contains(innerQuery, "persistK8sSources"):
			n := inFlight.Add(1)
			for {
				m := maxInFlight.Load()
				if n <= m || maxInFlight.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			inFlight.Add(-1)
			persistCount.Add(1)
			writeRemoteFetch(w, `{"type":"","data":{"persistK8sSources":true},"request_id":""}`)
		case strings.Contains(innerQuery, "source(sourceId") || strings.Contains(innerQuery, "GetSource"):
			if persistCount.Load() == 0 {
				writeRemoteFetch(w, emptySourceGetInner)
				return
			}
			writeRemoteFetch(w, populated)
		default:
			writeRemoteFetch(w, `{"type":"","data":{},"request_id":""}`)
		}
	}))
	defer server.Close()

	client, err := central.NewClient(context.Background(), central.ClientConfig{
		Hostname:           strings.TrimPrefix(server.URL, "https://"),
		Username:           "testuser",
		Password:           "testpass",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("build central client: %v", err)
	}
	cached := &cachedOdigosClient{client: client}
	r1 := &sourceResource{client: cached}
	r2 := &sourceResource{client: cached}

	schemaResp := &fwresource.SchemaResponse{}
	r1.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema

	var wg sync.WaitGroup
	var errCount atomic.Int32
	wg.Add(2)
	go func() {
		defer wg.Done()
		resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
		r1.Create(context.Background(), fwresource.CreateRequest{
			Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "alpha")},
		}, resp)
		if resp.Diagnostics.HasError() {
			errCount.Add(1)
		}
	}()
	go func() {
		defer wg.Done()
		resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
		r2.Create(context.Background(), fwresource.CreateRequest{
			Plan: tfsdk.Plan{Schema: schema, Raw: sourceRawValueWithStream(t, schema, "beta")},
		}, resp)
		if resp.Diagnostics.HasError() {
			errCount.Add(1)
		}
	}()
	wg.Wait()
	if persistCount.Load() != 1 {
		t.Errorf("persist calls = %d, want 1", persistCount.Load())
	}
	if errCount.Load() != 1 {
		t.Errorf("create errors = %d, want 1 (second resource rejected)", errCount.Load())
	}
	if maxInFlight.Load() != 1 {
		t.Errorf("maxInFlight concurrent persist = %d, want 1", maxInFlight.Load())
	}
}
