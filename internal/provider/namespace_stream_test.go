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
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	rsschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/odigos-io/odigos-central-client/central"
)

// persistCall records one persistK8sNamespaces mutation seen by the mock server.
type persistCall struct {
	Namespace string
	Stream    string
	Selected  bool
}

// startStreamMoveMockServer serves the handshake plus the namespace queries, recording
// every persistK8sNamespaces mutation in order. namespaceStreams is reported as the
// namespace's dataStreamNames on read-back.
func startStreamMoveMockServer(t *testing.T, namespaceStreams []string) (*httptest.Server, string, *[]persistCall) {
	t.Helper()

	var mu sync.Mutex
	calls := make([]persistCall, 0, 2)

	streamsJSON, err := json.Marshal(namespaceStreams)
	if err != nil {
		t.Fatalf("marshal streams: %v", err)
	}
	namespaceResponse := `{"type":"","data":{"computePlatform":{"k8sActualNamespaces":[{"name":"default","selected":true,"dataStreamNames":` +
		string(streamsJSON) + `,"sources":[]}]}},"request_id":""}`

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
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
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + onePlatformForNamespace + `},"errors":[]}`))
		case strings.Contains(query, "remoteFetch") || strings.Contains(query, "RemoteFetch"):
			innerQuery, _ := body.Variables["query"].(string)
			var inner string
			switch {
			case strings.Contains(innerQuery, "persistK8sNamespaces"):
				mu.Lock()
				calls = append(calls, decodePersistCalls(t, body.Variables)...)
				mu.Unlock()
				inner = `{"type":"","data":{"persistK8sNamespaces":true,"errors":[]},"request_id":""}`
			case strings.Contains(innerQuery, "k8sActualNamespaces"):
				inner = namespaceResponse
			default:
				inner = `{"type":"","data":{},"request_id":""}`
			}
			encoded, _ := json.Marshal(inner)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
		default:
			_, _ = w.Write([]byte(`{"data":{},"errors":[]}`))
		}
	}))

	return server, strings.TrimPrefix(server.URL, "https://"), &calls
}

// decodePersistCalls pulls the namespaces argument out of a remoteFetch envelope,
// whose variables are nested one level under "variables".
func decodePersistCalls(t *testing.T, outerVars map[string]any) []persistCall {
	t.Helper()

	innerVars, ok := outerVars["variables"].(map[string]any)
	if !ok {
		t.Fatalf("remoteFetch variables missing inner variables: %#v", outerVars)
	}
	raw, err := json.Marshal(innerVars["namespaces"])
	if err != nil {
		t.Fatalf("marshal namespaces: %v", err)
	}
	var inputs []struct {
		Namespace         string `json:"namespace"`
		CurrentStreamName string `json:"currentStreamName"`
		Selected          bool   `json:"selected"`
	}
	if err := json.Unmarshal(raw, &inputs); err != nil {
		t.Fatalf("unmarshal namespaces: %v", err)
	}

	result := make([]persistCall, 0, len(inputs))
	for _, in := range inputs {
		result = append(result, persistCall{Namespace: in.Namespace, Stream: in.CurrentStreamName, Selected: in.Selected})
	}
	return result
}

// newNamespaceResourceForTest wires a namespace resource to a client pointed at the
// mock server, and returns it alongside its schema.
func newNamespaceResourceForTest(t *testing.T, hostname string) (*namespaceResource, rsschema.Schema) {
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

	r := &namespaceResource{client: &cachedOdigosClient{client: client}}

	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema diagnostics: %v", schemaResp.Diagnostics)
	}

	return r, schemaResp.Schema
}

// namespaceRawValue builds a raw namespace object with the given stream, leaving the
// computed attributes null since Update only reads the identity fields and the stream.
func namespaceRawValue(t *testing.T, schema rsschema.Schema, stream string) tftypes.Value {
	t.Helper()

	objType, ok := schema.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("expected the namespace schema to be an object type")
	}

	return tftypes.NewValue(objType, map[string]tftypes.Value{
		"cluster_name":        tftypes.NewValue(tftypes.String, "TestCluster"),
		"name":                tftypes.NewValue(tftypes.String, "default"),
		"current_stream_name": tftypes.NewValue(tftypes.String, stream),
		"selected":            tftypes.NewValue(tftypes.Bool, true),
		"data_stream_names":   tftypes.NewValue(objType.AttributeTypes["data_stream_names"], nil),
		"sources":             tftypes.NewValue(objType.AttributeTypes["sources"], nil),
	})
}

// TestNamespaceUpdateAddsNewStreamBeforeRemovingOld pins the ordering that makes a
// stream move non-destructive. Odigos keeps stream membership as labels on the
// namespace's Source CRD and deletes the Source once its last stream label goes away,
// so removing the old stream first would drop instrumentation for the namespace and
// let the admission webhook re-add the "default" stream.
func TestNamespaceUpdateAddsNewStreamBeforeRemovingOld(t *testing.T) {
	server, hostname, calls := startStreamMoveMockServer(t, []string{"beta"})
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, hostname)

	priorState := namespaceRawValue(t, schema, "alpha")
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: priorState}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: namespaceRawValue(t, schema, "beta")},
		State: tfsdk.State{Schema: schema, Raw: priorState},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}

	want := []persistCall{
		{Namespace: "default", Stream: "beta", Selected: true},
		{Namespace: "default", Stream: "alpha", Selected: false},
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

// TestNamespaceUpdateKeepsPriorStreamWhenRemovalFails checks the partial-failure path.
// When the new stream is added but the old one cannot be removed, the namespace belongs
// to both streams, so Update must report an error and leave current_stream_name at its
// prior value for the next plan to retry.
func TestNamespaceUpdateKeepsPriorStreamWhenRemovalFails(t *testing.T) {
	var callCount int
	var mu sync.Mutex

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		query := strings.TrimSpace(body.Query)

		switch {
		case strings.Contains(query, "signIn") || strings.Contains(query, "SignIn"):
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
		case strings.Contains(query, "systemConfig") || strings.Contains(query, "GetSystemConfig"):
			_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.22.0"}},"errors":[]}`))
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + onePlatformForNamespace + `},"errors":[]}`))
		default:
			innerQuery, _ := body.Variables["query"].(string)
			if !strings.Contains(innerQuery, "persistK8sNamespaces") {
				encoded, _ := json.Marshal(`{"type":"","data":{},"request_id":""}`)
				_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
				return
			}
			// Succeed on the add, then report success=false on the removal.
			mu.Lock()
			callCount++
			succeeded := callCount == 1
			mu.Unlock()

			inner := `{"type":"","data":{"persistK8sNamespaces":false,"errors":[]},"request_id":""}`
			if succeeded {
				inner = `{"type":"","data":{"persistK8sNamespaces":true,"errors":[]},"request_id":""}`
			}
			encoded, _ := json.Marshal(inner)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
		}
	}))
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))

	priorState := namespaceRawValue(t, schema, "alpha")
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: priorState}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: namespaceRawValue(t, schema, "beta")},
		State: tfsdk.State{Schema: schema, Raw: priorState},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the namespace could not be removed from the old stream")
	}

	var stored NamespaceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if got := stored.CurrentStreamName.ValueString(); got != "alpha" {
		t.Errorf("current_stream_name = %q, want the prior value %q so the next plan retries the move", got, "alpha")
	}
}

// TestNamespaceReadRemovesWhenNotInStream covers out-of-band stream moves. The
// namespace object still exists in the API; only DataStreamNames shows it left
// the stream this resource manages, so Read must drop it from state.
func TestNamespaceReadRemovesWhenNotInStream(t *testing.T) {
	server, hostname, _ := startStreamMoveMockServer(t, []string{"beta"})
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, hostname)

	priorState := namespaceRawValue(t, schema, "alpha")
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: priorState}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: priorState},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("leaving a stream is not an error, got: %v", resp.Diagnostics)
	}
	if !resp.State.Raw.IsNull() {
		t.Error("expected Read to remove the resource from state when the namespace is no longer in the configured stream")
	}
}

// TestNamespaceReadKeepsWhenInStream checks the matching case: the namespace is
// still a member of the configured stream (even if other streams are present),
// so the resource stays in state.
func TestNamespaceReadKeepsWhenInStream(t *testing.T) {
	server, hostname, _ := startStreamMoveMockServer(t, []string{"alpha", "beta"})
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, hostname)

	priorState := namespaceRawValue(t, schema, "alpha")
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: priorState}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: priorState},
	}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", resp.Diagnostics)
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("expected the resource to remain in state while the namespace is still in the configured stream")
	}

	var stored NamespaceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if got := stored.CurrentStreamName.ValueString(); got != "alpha" {
		t.Errorf("current_stream_name = %q, want %q", got, "alpha")
	}
}

// TestNamespaceUpdateErrorsWhenReadbackMissesStream covers a persist that claimed
// success but a subsequent GetNamespace did not list the target stream. Update
// must not write the new stream into state, or Terraform would treat the move as done.
func TestNamespaceUpdateErrorsWhenReadbackMissesStream(t *testing.T) {
	server, hostname, _ := startStreamMoveMockServer(t, []string{"alpha"})
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, hostname)

	priorState := namespaceRawValue(t, schema, "alpha")
	resp := &fwresource.UpdateResponse{State: tfsdk.State{Schema: schema, Raw: priorState}}
	r.Update(context.Background(), fwresource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schema, Raw: namespaceRawValue(t, schema, "beta")},
		State: tfsdk.State{Schema: schema, Raw: priorState},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when read-back omitted the target stream")
	}

	var stored NamespaceResourceModel
	resp.Diagnostics.Append(resp.State.Get(context.Background(), &stored)...)
	if got := stored.CurrentStreamName.ValueString(); got != "alpha" {
		t.Errorf("current_stream_name = %q, want the prior value %q", got, "alpha")
	}
}

// TestNamespaceCreateErrorsWhenNotFound covers GetNamespace's documented
// "zero value, nil error" miss. Create must not write name = "" into state.
func TestNamespaceCreateErrorsWhenNotFound(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		query := strings.TrimSpace(body.Query)
		switch {
		case strings.Contains(query, "signIn") || strings.Contains(query, "SignIn"):
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
		case strings.Contains(query, "systemConfig") || strings.Contains(query, "GetSystemConfig"):
			_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.22.0"}},"errors":[]}`))
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + onePlatformForNamespace + `},"errors":[]}`))
		default:
			innerQuery, _ := body.Variables["query"].(string)
			var inner string
			if strings.Contains(innerQuery, "persistK8sNamespaces") {
				inner = `{"type":"","data":{"persistK8sNamespaces":true,"errors":[]},"request_id":""}`
			} else {
				inner = `{"type":"","data":{"computePlatform":{"k8sActualNamespaces":[]}},"request_id":""}`
			}
			encoded, _ := json.Marshal(inner)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
		}
	}))
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))

	resp := &fwresource.CreateResponse{State: tfsdk.State{Schema: schema}}
	r.Create(context.Background(), fwresource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schema, Raw: namespaceRawValue(t, schema, "default")},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when the namespace was not found after persist")
	}
	if !resp.State.Raw.IsNull() {
		var stored NamespaceResourceModel
		_ = resp.State.Get(context.Background(), &stored)
		if stored.Name.ValueString() == "" && !stored.Name.IsNull() {
			t.Error("Create wrote name = \"\" into state; it must leave state unset")
		}
	}
}

// TestNamespaceReadKeepsStateOnGetError is the transient-error path. Read must
// not RemoveResource: Terraform keeps prior state when Read errors, and pairing
// the two would become data loss if that rule ever changed.
func TestNamespaceReadKeepsStateOnGetError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		query := strings.TrimSpace(body.Query)
		switch {
		case strings.Contains(query, "signIn") || strings.Contains(query, "SignIn"):
			_, _ = w.Write([]byte(`{"data":{"signIn":{"accessToken":"mock-token","idToken":"","status":"","user":{},"__typename":""}},"errors":[]}`))
		case strings.Contains(query, "systemConfig") || strings.Contains(query, "GetSystemConfig"):
			_, _ = w.Write([]byte(`{"data":{"systemConfig":{"isInitialized":true,"version":"v1.22.0"}},"errors":[]}`))
		case strings.Contains(query, "computePlatforms") || strings.Contains(query, "GetComputePlatforms"):
			_, _ = w.Write([]byte(`{"data":{"computePlatforms":` + onePlatformForNamespace + `},"errors":[]}`))
		default:
			innerQuery, _ := body.Variables["query"].(string)
			if strings.Contains(innerQuery, "k8sActualNamespaces") {
				encoded, _ := json.Marshal(`null`)
				_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
				return
			}
			encoded, _ := json.Marshal(`{"type":"","data":{},"request_id":""}`)
			_, _ = w.Write([]byte(`{"data":{"remoteFetch":` + string(encoded) + `},"errors":[]}`))
		}
	}))
	defer server.Close()

	r, schema := newNamespaceResourceForTest(t, strings.TrimPrefix(server.URL, "https://"))

	priorState := namespaceRawValue(t, schema, "default")
	resp := &fwresource.ReadResponse{State: tfsdk.State{Schema: schema, Raw: priorState}}
	r.Read(context.Background(), fwresource.ReadRequest{
		State: tfsdk.State{Schema: schema, Raw: priorState},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when GetNamespace failed")
	}
	if resp.State.Raw.IsNull() {
		t.Error("Read removed the resource from state on a transient GetNamespace error")
	}
}

func TestNamespaceReadKeepsStateOnUnsupportedCluster(t *testing.T) {
	client := &cachedOdigosClient{}
	client.proxies.Store("TestCluster", (*central.ProxyClient)(nil))
	r := &namespaceResource{client: client}

	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema

	prior := namespaceRawValue(t, schema, "default")
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

func TestNamespaceDeleteErrorsOnUnsupportedCluster(t *testing.T) {
	client := &cachedOdigosClient{}
	client.proxies.Store("TestCluster", (*central.ProxyClient)(nil))
	r := &namespaceResource{client: client}

	schemaResp := &fwresource.SchemaResponse{}
	r.Schema(context.Background(), fwresource.SchemaRequest{}, schemaResp)
	schema := schemaResp.Schema

	prior := namespaceRawValue(t, schema, "default")
	resp := &fwresource.DeleteResponse{}
	r.Delete(context.Background(), fwresource.DeleteRequest{
		State: tfsdk.State{Schema: schema, Raw: prior},
	}, resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error when deleting a namespace on an unsupported cluster")
	}
}
