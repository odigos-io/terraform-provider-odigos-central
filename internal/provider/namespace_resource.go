// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/odigos-io/odigos-central-client/central"
	odigostypes "github.com/odigos-io/odigos-central-client/types"
)

var (
	_ resource.Resource                = &namespaceResource{}
	_ resource.ResourceWithConfigure   = &namespaceResource{}
	_ resource.ResourceWithModifyPlan  = &namespaceResource{}
	_ resource.ResourceWithImportState = &namespaceResource{}
)

func NewNamespaceResource() resource.Resource {
	return &namespaceResource{}
}

type namespaceResource struct {
	client *cachedOdigosClient
}

// NamespaceResourceModel matches the shape of odigos k8sActualNamespaceDetail plus
// provider fields (cluster_name, current_stream_name) used for lookup and persist.
type NamespaceResourceModel struct {
	ClusterName       types.String `tfsdk:"cluster_name"`
	CurrentStreamName types.String `tfsdk:"current_stream_name"`
	Name              types.String `tfsdk:"name"`
	Selected          types.Bool   `tfsdk:"selected"`
	DataStreamNames   types.List   `tfsdk:"data_stream_names"`
	Sources           types.List   `tfsdk:"sources"`
}

// NamespaceSourceModel matches odigos namespaceSource (each item in k8sActualNamespaceDetail.Sources).
type NamespaceSourceModel struct {
	Namespace         types.String `tfsdk:"namespace"`
	Kind              types.String `tfsdk:"kind"`
	Name              types.String `tfsdk:"name"`
	DataStreamNames   types.List   `tfsdk:"data_stream_names"`
	Selected          types.Bool   `tfsdk:"selected"`
	NumberOfInstances types.Int64  `tfsdk:"number_of_instances"`
}

func (r *namespaceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_namespace"
}

func (r *namespaceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enables or disables Odigos instrumentation for a Kubernetes namespace on one " +
			"data stream. Set `selected` to false to disable without destroying the resource. Destroying " +
			"the resource removes it from `current_stream_name` only; other streams in `data_stream_names` " +
			"(for example membership added outside this resource) are left unchanged. Changing " +
			"`cluster_name` or `name` forces replacement. Changing `current_stream_name` moves the " +
			"namespace between streams.",
		Attributes: map[string]schema.Attribute{
			"cluster_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the cluster (compute platform).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the namespace.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"current_stream_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the data stream to use when persisting namespace selection.",
			},
			"selected": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the namespace is selected for instrumentation. Set to false to disable. Defaults to true.",
			},
			"data_stream_names": schema.ListAttribute{
				Computed:    true,
				Description: "The names of the data streams.",
				ElementType: types.StringType,
			},
			"sources": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Sources in this namespace.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"namespace": schema.StringAttribute{
							Computed:    true,
							Description: "The namespace of the source.",
						},
						"kind": schema.StringAttribute{
							Computed:    true,
							Description: "The kind of the source (e.g. Deployment).",
						},
						"name": schema.StringAttribute{
							Computed:    true,
							Description: "The name of the source.",
						},
						"data_stream_names": schema.ListAttribute{
							Computed:    true,
							Description: "The names of the data streams for this source.",
							ElementType: types.StringType,
						},
						"selected": schema.BoolAttribute{
							Computed:    true,
							Description: "Whether the source is selected.",
						},
						"number_of_instances": schema.Int64Attribute{
							Computed:    true,
							Description: "The number of instances (matches numberOfInstances).",
						},
					},
				},
			},
		},
	}
}

func (r *namespaceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*cachedOdigosClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *cachedOdigosClient, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = client
}

// persistNamespaceStream adds or removes the namespace's membership in a single data
// stream. The mutation reports failure through a success flag rather than an error, so
// both are folded into the returned error.
func persistNamespaceStream(ctx context.Context, proxy *central.ProxyClient, namespace, streamName string, selected bool) error {
	return persistNamespaceStreams(ctx, proxy, namespace, []string{streamName}, selected)
}

func persistNamespaceStreams(ctx context.Context, proxy *central.ProxyClient, namespace string, streams []string, selected bool) error {
	if len(streams) == 0 {
		return nil
	}
	inputs := make([]odigostypes.NamespaceInput, 0, len(streams))
	for _, stream := range streams {
		inputs = append(inputs, odigostypes.NamespaceInput{
			Namespace:         namespace,
			CurrentStreamName: stream,
			Selected:          selected,
		})
	}
	success, err := proxy.PersistNamespaceSources(ctx, inputs)
	if err != nil {
		return err
	}
	if !success {
		return errors.New("the API returned success=false")
	}
	return nil
}

// ModifyPlan marks the computed attributes unknown when the data stream changes.
// Terraform proposes the prior values for computed attributes on an in-place update,
// but moving streams rewrites data_stream_names and the per-source stream lists, so
// leaving the prior values in the plan would make the applied state disagree with it.
func (r *namespaceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Only in-place updates need this; create and destroy lack one side of the pair.
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state NamespaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	streamChanged := !plan.CurrentStreamName.Equal(state.CurrentStreamName)
	selectedChanged := !plan.Selected.Equal(state.Selected)
	if !streamChanged && !selectedChanged {
		return
	}

	plan.DataStreamNames = types.ListUnknown(types.StringType)
	plan.Sources = types.ListUnknown(namespaceSourceObjectType)

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *namespaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan NamespaceResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	proxy, proxyDiags := r.client.ProxyByName(ctx, plan.ClusterName.ValueString())
	resp.Diagnostics.Append(proxyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if proxy == nil {
		resp.Diagnostics.AddError(
			"Cannot Create Namespace on Unsupported Cluster",
			"Cluster "+plan.ClusterName.ValueString()+" is not supported by this provider; cannot create odigos-central_namespace.",
		)
		return
	}

	if err := persistNamespaceStream(ctx, proxy, plan.Name.ValueString(), plan.CurrentStreamName.ValueString(), plan.Selected.ValueBool()); err != nil {
		resp.Diagnostics.AddError(
			"Error persisting namespace sources",
			"Could not persist namespace sources, unexpected error: "+err.Error(),
		)
		return
	}

	detail, err := proxy.GetNamespace(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading namespace after create",
			"Could not get namespace, unexpected error: "+err.Error(),
		)
		return
	}

	// GetNamespace returns a zero value and a nil error when no namespace matches.
	if detail.Name == "" {
		resp.Diagnostics.AddError(
			"Namespace Not Found",
			"Namespace "+plan.Name.ValueString()+" was not found in cluster "+
				plan.ClusterName.ValueString()+" after persist.",
		)
		return
	}

	state, mapDiags := namespaceDetailToModel(ctx, plan.ClusterName, plan.CurrentStreamName, detail)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *namespaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NamespaceResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	proxy, proxyDiags := r.client.ProxyByName(ctx, state.ClusterName.ValueString())
	resp.Diagnostics.Append(proxyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if proxy == nil {
		// Unsupported cluster is not "namespace gone". Keep prior state; the
		// warning from ProxyByName explains why computed attributes were not refreshed.
		return
	}

	tflog.Trace(ctx, "Namespace: "+state.Name.ValueString())

	detail, err := proxy.GetNamespace(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error getting namespace",
			"Could not get namespace, unexpected error: "+err.Error(),
		)
		return
	}

	if detail.Name == "" {
		resp.Diagnostics.AddWarning(
			"Namespace No Longer Exists",
			"The namespace was not found or has been removed. It will be removed from the Terraform state.",
		)
		resp.State.RemoveResource(ctx)
		return
	}

	newState, mapDiags := namespaceDetailToModel(ctx, state.ClusterName, state.CurrentStreamName, detail)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

// Update moves the namespace between data streams; cluster_name and name force
// replacement, so current_stream_name is the only attribute that reaches here.
//
// Odigos records stream membership as labels on the namespace's Source CRD, so the new
// stream must be added before the old one is removed. In the reverse order the Source
// would briefly carry no stream label, which deletes it (dropping instrumentation for
// every workload in the namespace) and causes the admission webhook to re-add the
// "default" stream.
func (r *namespaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state NamespaceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	proxy, proxyDiags := r.client.ProxyByName(ctx, plan.ClusterName.ValueString())
	resp.Diagnostics.Append(proxyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if proxy == nil {
		resp.Diagnostics.AddError(
			"Cannot Update Namespace on Unsupported Cluster",
			"Cluster "+plan.ClusterName.ValueString()+" is not supported by this provider; cannot update odigos-central_namespace.",
		)
		return
	}

	namespace := plan.Name.ValueString()
	oldStream := state.CurrentStreamName.ValueString()
	newStream := plan.CurrentStreamName.ValueString()
	selected := plan.Selected.ValueBool()

	if oldStream != newStream {
		if err := persistNamespaceStream(ctx, proxy, namespace, newStream, selected); err != nil {
			resp.Diagnostics.AddError(
				"Error Adding Namespace To Data Stream",
				"Could not add namespace "+namespace+" to data stream "+newStream+": "+err.Error(),
			)
			return
		}

		if err := persistNamespaceStream(ctx, proxy, namespace, oldStream, false); err != nil {
			// Returning without setting state leaves current_stream_name at the old
			// value, so the next plan retries the move instead of reporting success.
			resp.Diagnostics.AddError(
				"Error Removing Namespace From Previous Data Stream",
				"Namespace "+namespace+" was added to data stream "+newStream+
					" but could not be removed from "+oldStream+": "+err.Error()+
					". The namespace currently belongs to both streams; apply again to retry.",
			)
			return
		}
	} else if !plan.Selected.Equal(state.Selected) {
		if err := persistNamespaceStream(ctx, proxy, namespace, newStream, selected); err != nil {
			resp.Diagnostics.AddError(
				"Error Updating Namespace Selection",
				"Could not set selected="+fmt.Sprint(selected)+" on namespace "+namespace+": "+err.Error(),
			)
			return
		}
	}

	detail, err := proxy.GetNamespace(ctx, namespace)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading namespace after update",
			"Could not get namespace, unexpected error: "+err.Error(),
		)
		return
	}

	if selected && newStream != "" && !slices.Contains(detail.DataStreamNames, newStream) {
		resp.Diagnostics.AddError(
			"Namespace Missing From Target Data Stream",
			"Namespace "+namespace+" was persisted into data stream "+newStream+
				" but a subsequent read did not list that stream. The next plan will retry.",
		)
		return
	}

	newState, mapDiags := namespaceDetailToModel(ctx, plan.ClusterName, plan.CurrentStreamName, detail)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *namespaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NamespaceResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	proxy, proxyDiags := r.client.ProxyByName(ctx, state.ClusterName.ValueString())
	resp.Diagnostics.Append(proxyDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if proxy == nil {
		resp.Diagnostics.AddError(
			"Cannot Delete Namespace on Unsupported Cluster",
			"Cluster "+state.ClusterName.ValueString()+" is not supported by this provider; cannot delete odigos-central_namespace.",
		)
		return
	}

	if err := persistNamespaceStream(ctx, proxy, state.Name.ValueString(), state.CurrentStreamName.ValueString(), false); err != nil {
		resp.Diagnostics.AddError(
			"Error deleting namespace selection",
			"Could not remove namespace from data stream "+state.CurrentStreamName.ValueString()+
				", unexpected error: "+err.Error(),
		)
		return
	}
}

func (r *namespaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, diags := parseSlashID(req.ID, 3, "cluster_name/namespace/current_stream_name")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("current_stream_name"), parts[2])...)
}

var namespaceSourceObjectType = types.ObjectType{
	AttrTypes: map[string]attr.Type{
		"namespace":           types.StringType,
		"kind":                types.StringType,
		"name":                types.StringType,
		"data_stream_names":   types.ListType{ElemType: types.StringType},
		"selected":            types.BoolType,
		"number_of_instances": types.Int64Type,
	},
}

// namespaceDetailToModel maps odigos K8sActualNamespaceDetail to NamespaceResourceModel.
// Nil slices become empty lists so "no data streams" matches mapSourceToModel.
func namespaceDetailToModel(ctx context.Context, clusterName, currentStreamName types.String, detail odigostypes.K8sActualNamespaceDetail) (NamespaceResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	model := NamespaceResourceModel{
		ClusterName:       clusterName,
		CurrentStreamName: currentStreamName,
		Name:              types.StringValue(detail.Name),
		Selected:          types.BoolValue(detail.Selected),
	}

	streamNames := detail.DataStreamNames
	if streamNames == nil {
		streamNames = []string{}
	}
	var streamDiags diag.Diagnostics
	model.DataStreamNames, streamDiags = types.ListValueFrom(ctx, types.StringType, streamNames)
	diags.Append(streamDiags...)
	if diags.HasError() {
		return model, diags
	}

	sourceElems := make([]attr.Value, len(detail.Sources))
	for i, s := range detail.Sources {
		sourceStreams := s.DataStreamNames
		if sourceStreams == nil {
			sourceStreams = []string{}
		}
		dsList, dsDiags := types.ListValueFrom(ctx, types.StringType, sourceStreams)
		diags.Append(dsDiags...)
		if diags.HasError() {
			return model, diags
		}

		obj, objDiags := types.ObjectValue(namespaceSourceObjectType.AttrTypes, map[string]attr.Value{
			"namespace":           types.StringValue(s.Namespace),
			"kind":                types.StringValue(s.Kind),
			"name":                types.StringValue(s.Name),
			"data_stream_names":   dsList,
			"selected":            types.BoolValue(s.Selected),
			"number_of_instances": types.Int64Value(int64(s.NumberOfInstances)),
		})
		diags.Append(objDiags...)
		if diags.HasError() {
			return model, diags
		}
		sourceElems[i] = obj
	}

	var sourcesDiags diag.Diagnostics
	model.Sources, sourcesDiags = types.ListValue(namespaceSourceObjectType, sourceElems)
	diags.Append(sourcesDiags...)
	return model, diags
}
