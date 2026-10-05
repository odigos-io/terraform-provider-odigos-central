// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

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
	_ resource.Resource                = &sourceResource{}
	_ resource.ResourceWithConfigure   = &sourceResource{}
	_ resource.ResourceWithModifyPlan  = &sourceResource{}
	_ resource.ResourceWithImportState = &sourceResource{}
)

func NewSourceResource() resource.Resource {
	return &sourceResource{}
}

type sourceResource struct {
	client *cachedOdigosClient
}

// Container and Condition ObjectTypes for handling unknown values in plans.
var (
	containerObjectType = types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"container_name":          types.StringType,
			"language":                types.StringType,
			"runtime_version":         types.StringType,
			"overridden":              types.BoolType,
			"instrumented":            types.BoolType,
			"instrumentation_message": types.StringType,
			"otel_distro_name":        types.StringType,
		},
	}
	conditionObjectType = types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"status":               types.StringType,
			"type":                 types.StringType,
			"reason":               types.StringType,
			"message":              types.StringType,
			"last_transition_time": types.StringType,
		},
	}
)

type SourceResourceModel struct {
	ClusterName       types.String `tfsdk:"cluster_name"`
	CurrentStreamName types.String `tfsdk:"current_stream_name"`
	Namespace         types.String `tfsdk:"namespace"`
	Name              types.String `tfsdk:"name"`
	Kind              types.String `tfsdk:"kind"`
	Selected          types.Bool   `tfsdk:"selected"`
	DataStreamNames   types.List   `tfsdk:"data_stream_names"`
	OtelServiceName   types.String `tfsdk:"otel_service_name"`
	ClusterID         types.String `tfsdk:"cluster_id"`
	Containers        types.List   `tfsdk:"containers"`
	Conditions        types.List   `tfsdk:"conditions"`
}

func (r *sourceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_source"
}

func (r *sourceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Enables or disables Odigos instrumentation for a Kubernetes workload " +
			"(Deployment, StatefulSet, DaemonSet, and so on) on one data stream. Set `selected` to false " +
			"to disable without destroying the resource. Destroying the resource removes it from " +
			"`current_stream_name` only; other streams in `data_stream_names` (for example membership " +
			"added outside this resource) are left unchanged. Changing `cluster_name`, `name`, `kind`, " +
			"or `namespace` forces replacement. Changing `current_stream_name` moves the source between streams. " +
			"Odigos allows one source per workload: a second resource for the same Deployment (or other kind) " +
			"on another stream is rejected. Change `current_stream_name` on the existing resource to move it. " +
			"Creating or destroying several resources for the same workload runs one at a time.",
		Attributes: map[string]schema.Attribute{
			"cluster_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the cluster",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the source",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"kind": schema.StringAttribute{
				Required:    true,
				Description: "The type of the source",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"namespace": schema.StringAttribute{
				Required:    true,
				Description: "The namespace of the source",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"current_stream_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the data stream to use when persisting source selection.",
			},
			"selected": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the source is selected for instrumentation. Set to false to disable. Defaults to true.",
			},
			"data_stream_names": schema.ListAttribute{
				Computed:    true,
				Description: "The names of the data streams",
				ElementType: types.StringType,
			},
			"otel_service_name": schema.StringAttribute{
				Computed:    true,
				Description: "The name of the otel service",
			},
			"cluster_id": schema.StringAttribute{
				Computed:    true,
				Description: "The id of the cluster",
			},
			"containers": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The containers of the source",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"container_name": schema.StringAttribute{
							Computed:    true,
							Description: "The name of the container",
						},
						"language": schema.StringAttribute{
							Computed:    true,
							Description: "The language of the container",
						},
						"runtime_version": schema.StringAttribute{
							Computed:    true,
							Description: "The runtime version of the container",
						},
						"overridden": schema.BoolAttribute{
							Computed:    true,
							Description: "Whether the container is overridden",
						},
						"instrumented": schema.BoolAttribute{
							Computed:    true,
							Description: "Whether the container is instrumented",
						},
						"instrumentation_message": schema.StringAttribute{
							Computed:    true,
							Description: "The instrumentation message of the container",
						},
						"otel_distro_name": schema.StringAttribute{
							Computed:    true,
							Description: "The otel distro name of the container",
						},
					},
				},
			},
			"conditions": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The conditions of the source",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"status": schema.StringAttribute{
							Computed:    true,
							Description: "The status of the condition",
						},
						"type": schema.StringAttribute{
							Computed:    true,
							Description: "The type of the condition",
						},
						"reason": schema.StringAttribute{
							Computed:    true,
							Description: "The reason of the condition",
						},
						"message": schema.StringAttribute{
							Computed:    true,
							Description: "The message of the condition",
						},
						"last_transition_time": schema.StringAttribute{
							Computed:    true,
							Description: "The last transition time of the condition",
						},
					},
				},
			},
		},
	}
}

func (r *sourceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sourceResource) lockModel(m SourceResourceModel) func() {
	return r.client.lockPersist(sourcePersistKey(
		m.ClusterName.ValueString(),
		m.Namespace.ValueString(),
		m.Kind.ValueString(),
		m.Name.ValueString(),
	))
}

// ModifyPlan marks computed attributes unknown when the data stream changes.
// Terraform proposes the prior values for computed attributes on an in-place update,
// but moving streams rewrites data_stream_names and may change otel_service_name.
func (r *sourceResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var plan, state SourceResourceModel
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
	plan.OtelServiceName = types.StringUnknown()
	plan.Containers = types.ListUnknown(containerObjectType)
	plan.Conditions = types.ListUnknown(conditionObjectType)

	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// persistSource writes one source-selection update. The mutation reports failure
// through a success flag rather than an error, so both are folded into the returned error.
func persistSource(ctx context.Context, proxy *central.ProxyClient, input odigostypes.SourceInput) error {
	return persistSources(ctx, proxy, []odigostypes.SourceInput{input})
}

func persistSources(ctx context.Context, proxy *central.ProxyClient, inputs []odigostypes.SourceInput) error {
	if len(inputs) == 0 {
		return nil
	}
	success, err := proxy.PersistSources(ctx, inputs)
	if err != nil {
		return err
	}
	if !success {
		return errors.New("the API returned success=false")
	}
	return nil
}

func existingSourceConflict(source odigostypes.Source, plannedStream string) string {
	if source.Name == "" || source.Namespace == "" || source.Kind == "" || len(source.DataStreamNames) == 0 {
		return ""
	}
	return source.Kind + "/" + source.Name + " in namespace " + source.Namespace +
		" is already an Odigos source on data stream " + existingMembershipStream(source.DataStreamNames, plannedStream) +
		". Odigos allows one source per workload. Use a single odigos-central_source and change current_stream_name to move it, or terraform import the existing source."
}

func (r *sourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SourceResourceModel
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
			"Cannot Create Source on Unsupported Cluster",
			"Cluster "+plan.ClusterName.ValueString()+" is not supported by this provider; cannot create odigos-central_source.",
		)
		return
	}

	unlock := r.lockModel(plan)
	defer unlock()

	source, err := proxy.GetSource(ctx, odigostypes.SourceID{
		Name: plan.Name.ValueString(), Namespace: plan.Namespace.ValueString(), Kind: plan.Kind.ValueString(),
	})
	if err != nil && !sourceNotYetCreated(err) {
		resp.Diagnostics.AddError(
			"Error reading source before create",
			"Could not get source, unexpected error: "+err.Error(),
		)
		return
	}
	if err == nil {
		if msg := existingSourceConflict(source, plan.CurrentStreamName.ValueString()); msg != "" {
			resp.Diagnostics.AddError("Source Already Exists", msg)
			return
		}
	}

	if err := persistSource(ctx, proxy, odigostypes.SourceInput{
		Name:              plan.Name.ValueString(),
		Namespace:         plan.Namespace.ValueString(),
		Kind:              plan.Kind.ValueString(),
		CurrentStreamName: plan.CurrentStreamName.ValueString(),
		Selected:          plan.Selected.ValueBool(),
	}); err != nil {
		resp.Diagnostics.AddError(
			"Error persisting source",
			"Could not persist source, unexpected error: "+annotateDuplicateSourceError(err),
		)
		return
	}

	source, err = proxy.GetSource(ctx, odigostypes.SourceID{
		Name: plan.Name.ValueString(), Namespace: plan.Namespace.ValueString(), Kind: plan.Kind.ValueString(),
	})
	if err != nil {
		if sourceNotYetCreated(err) && !plan.Selected.ValueBool() {
			mapDiags := mapSourceToModel(&plan, proxy.ID(), odigostypes.Source{})
			resp.Diagnostics.Append(mapDiags...)
			if resp.Diagnostics.HasError() {
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading source after create",
			"Could not get source, unexpected error: "+err.Error(),
		)
		return
	}
	if source.Name == "" || source.Namespace == "" || source.Kind == "" {
		resp.Diagnostics.AddError(
			"Source Not Found After Create",
			"Source "+plan.Kind.ValueString()+"/"+plan.Name.ValueString()+" in namespace "+
				plan.Namespace.ValueString()+" was not found after persist.",
		)
		return
	}

	mapDiags := mapSourceToModel(&plan, proxy.ID(), source)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *sourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SourceResourceModel
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
		// Unsupported cluster is not "source gone". Keep prior state; the
		// warning from ProxyByName explains why computed attributes were not refreshed.
		return
	}

	tflog.Trace(ctx, "Name: "+state.Name.ValueString())
	source, err := proxy.GetSource(ctx, odigostypes.SourceID{
		Name: state.Name.ValueString(), Namespace: state.Namespace.ValueString(), Kind: state.Kind.ValueString(),
	})
	if err != nil {
		if sourceNotYetCreated(err) {
			mapDiags := mapSourceToModel(&state, proxy.ID(), odigostypes.Source{})
			resp.Diagnostics.Append(mapDiags...)
			if resp.Diagnostics.HasError() {
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
			return
		}
		resp.Diagnostics.AddError(
			"Error getting source",
			"Could not get source, unexpected error: "+err.Error(),
		)
		return
	}

	if source.Name == "" || source.Namespace == "" || source.Kind == "" {
		resp.Diagnostics.AddWarning(
			"Source No Longer Exists",
			"The source was not found or has been removed. It will be removed from the Terraform state.",
		)
		resp.State.RemoveResource(ctx)
		return
	}

	// cluster_id is Central's identifier for the compute platform. Re-registering
	// a cluster assigns a new ID without deleting the workload, so a mismatch
	// here is not "source gone". Refresh cluster_id from the current proxy and
	// keep the resource.
	if !state.ClusterID.IsNull() && !state.ClusterID.IsUnknown() &&
		state.ClusterID.ValueString() != "" && state.ClusterID.ValueString() != proxy.ID() {
		resp.Diagnostics.AddWarning(
			"Cluster ID Changed",
			"Cluster "+state.ClusterName.ValueString()+" now has id "+proxy.ID()+
				" (was "+state.ClusterID.ValueString()+
				"). The source still exists and will stay in state with the new cluster_id.",
		)
	}
	tflog.Trace(ctx, "Namespace: "+source.Namespace)

	mapDiags := mapSourceToModel(&state, proxy.ID(), source)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *sourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state SourceResourceModel
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
			"Cannot Update Source on Unsupported Cluster",
			"Cluster "+plan.ClusterName.ValueString()+" is not supported by this provider; cannot update odigos-central_source.",
		)
		return
	}

	oldStream := state.CurrentStreamName.ValueString()
	newStream := plan.CurrentStreamName.ValueString()
	selected := plan.Selected.ValueBool()
	input := odigostypes.SourceInput{
		Name:      plan.Name.ValueString(),
		Namespace: plan.Namespace.ValueString(),
		Kind:      plan.Kind.ValueString(),
	}

	if oldStream != newStream {
		input.CurrentStreamName = newStream
		input.Selected = selected
		if err := persistSource(ctx, proxy, input); err != nil {
			resp.Diagnostics.AddError(
				"Error Adding Source To Data Stream",
				"Could not add source to data stream "+newStream+": "+err.Error(),
			)
			return
		}

		input.CurrentStreamName = oldStream
		input.Selected = false
		if err := persistSource(ctx, proxy, input); err != nil {
			resp.Diagnostics.AddError(
				"Error Removing Source From Previous Data Stream",
				"Source "+input.Kind+"/"+input.Name+" was added to data stream "+newStream+
					" but could not be removed from "+oldStream+": "+err.Error()+
					". The source currently belongs to both streams; apply again to retry.",
			)
			return
		}
	} else if !plan.Selected.Equal(state.Selected) {
		input.CurrentStreamName = newStream
		input.Selected = selected
		if err := persistSource(ctx, proxy, input); err != nil {
			resp.Diagnostics.AddError(
				"Error Updating Source Selection",
				"Could not set selected="+fmt.Sprint(selected)+" on source: "+err.Error(),
			)
			return
		}
	}

	source, err := proxy.GetSource(ctx, odigostypes.SourceID{
		Name: input.Name, Namespace: input.Namespace, Kind: input.Kind,
	})
	if err != nil {
		if sourceNotYetCreated(err) && !selected {
			mapDiags := mapSourceToModel(&plan, proxy.ID(), odigostypes.Source{})
			resp.Diagnostics.Append(mapDiags...)
			if resp.Diagnostics.HasError() {
				return
			}
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			return
		}
		resp.Diagnostics.AddError(
			"Error reading source after update",
			"Could not get source, unexpected error: "+err.Error(),
		)
		return
	}

	if selected && newStream != "" && !slices.Contains(source.DataStreamNames, newStream) {
		resp.Diagnostics.AddError(
			"Source Missing From Target Data Stream",
			"Source "+input.Kind+"/"+input.Name+" was persisted into data stream "+newStream+
				" but a subsequent read did not list that stream. The next plan will retry.",
		)
		return
	}

	mapDiags := mapSourceToModel(&plan, proxy.ID(), source)
	resp.Diagnostics.Append(mapDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SourceResourceModel
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
			"Cannot Delete Source on Unsupported Cluster",
			"Cluster "+state.ClusterName.ValueString()+" is not supported by this provider; cannot delete odigos-central_source.",
		)
		return
	}

	unlock := r.lockModel(state)
	defer unlock()

	if err := persistSource(ctx, proxy, odigostypes.SourceInput{
		Name:              state.Name.ValueString(),
		Namespace:         state.Namespace.ValueString(),
		Kind:              state.Kind.ValueString(),
		CurrentStreamName: state.CurrentStreamName.ValueString(),
		Selected:          false,
	}); err != nil {
		resp.Diagnostics.AddError(
			"Error deleting source",
			"Could not remove source from data stream "+state.CurrentStreamName.ValueString()+
				", unexpected error: "+annotateDuplicateSourceError(err),
		)
		return
	}
}

func (r *sourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, diags := parseSlashID(req.ID, 5, "cluster_name/namespace/kind/name/current_stream_name")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("cluster_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("namespace"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("kind"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), parts[3])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("current_stream_name"), parts[4])...)
}

// mapSourceToModel maps the API source response and clusterID onto the given SourceResourceModel.
// It returns diagnostics if any conversion errors occur.
func mapSourceToModel(model *SourceResourceModel, clusterID string, source odigostypes.Source) diag.Diagnostics {
	var diags diag.Diagnostics

	model.ClusterID = types.StringValue(clusterID)
	model.OtelServiceName = types.StringValue(source.OtelServiceName)
	model.Selected = types.BoolValue(source.Selected)

	elems := make([]attr.Value, len(source.DataStreamNames))
	for i, s := range source.DataStreamNames {
		elems[i] = types.StringValue(s)
	}
	model.DataStreamNames, diags = types.ListValue(types.StringType, elems)
	if diags.HasError() {
		return diags
	}

	containerElems := make([]attr.Value, len(source.Containers))
	for i, container := range source.Containers {
		obj, objDiags := types.ObjectValue(
			containerObjectType.AttrTypes,
			map[string]attr.Value{
				"container_name":          types.StringValue(container.ContainerName),
				"language":                types.StringValue(container.Language),
				"runtime_version":         types.StringValue(container.RuntimeVersion),
				"overridden":              types.BoolValue(container.Overridden),
				"instrumented":            types.BoolValue(container.Instrumented),
				"instrumentation_message": types.StringValue(container.InstrumentationMessage),
				"otel_distro_name":        types.StringValue(container.OtelDistroName),
			},
		)
		diags.Append(objDiags...)
		if diags.HasError() {
			return diags
		}
		containerElems[i] = obj
	}
	var containerDiags diag.Diagnostics
	model.Containers, containerDiags = types.ListValue(containerObjectType, containerElems)
	diags.Append(containerDiags...)
	if diags.HasError() {
		return diags
	}

	conditionElems := make([]attr.Value, len(source.Conditions))
	for i, condition := range source.Conditions {
		var transitionTime string
		if condition.LastTransitionTime != nil {
			transitionTime = condition.LastTransitionTime.Format(time.RFC3339)
		}
		obj, objDiags := types.ObjectValue(
			conditionObjectType.AttrTypes,
			map[string]attr.Value{
				"status":               types.StringValue(condition.Status),
				"type":                 types.StringValue(condition.Type),
				"reason":               types.StringValue(condition.Reason),
				"message":              types.StringValue(condition.Message),
				"last_transition_time": types.StringValue(transitionTime),
			},
		)
		diags.Append(objDiags...)
		if diags.HasError() {
			return diags
		}
		conditionElems[i] = obj
	}
	var conditionDiags diag.Diagnostics
	model.Conditions, conditionDiags = types.ListValue(conditionObjectType, conditionElems)
	diags.Append(conditionDiags...)
	if diags.HasError() {
		return diags
	}

	return diags
}
