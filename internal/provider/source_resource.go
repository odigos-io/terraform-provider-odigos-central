package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	odigosclient "github.com/odigos-io/odigos-central-client"
)

var (
	_ resource.Resource              = &sourceResource{}
	_ resource.ResourceWithConfigure = &sourceResource{}
)

func NewSourceResource() resource.Resource {
	return &sourceResource{}
}

type sourceResource struct {
	client *cachedOdigosClient
}

// type sourceResourceModel struct {
// 	Source SourceModel `tfsdk:"source"`
// }

// Container and Condition ObjectTypes for handling unknown values in plans
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

// type SourceModel struct {
type SourceResourceModel struct {
	ClusterName       types.String `tfsdk:"cluster_name"`
	CurrentStreamName types.String `tfsdk:"current_stream_name"`
	Namespace         types.String `tfsdk:"namespace"`
	Name              types.String `tfsdk:"name"`
	Kind              types.String `tfsdk:"kind"`
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
				Description: "The name of the data stream",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
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

func (r *sourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {

	var plan SourceResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID, err := getClusterID(ctx, r.client, plan.ClusterName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error getting cluster ID",
			"Could not get cluster ID, unexpected error: "+err.Error(),
		)
		return
	}

	sourceToPersist := []odigosclient.SourceInput{
		{
			Name:              plan.Name.ValueString(),
			Namespace:         plan.Namespace.ValueString(),
			Kind:              plan.Kind.ValueString(),
			CurrentStreamName: plan.CurrentStreamName.ValueString(),
			Selected:          true,
		},
	}

	_, err = r.client.PersistSources(ctx, clusterID, sourceToPersist)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error persisting source",
			"Could not persist source, unexpected error: "+err.Error(),
		)
		return
	}

	source, err := r.client.GetSource(ctx, clusterID, odigosclient.SourceId{
		Name: plan.Name.ValueString(), Namespace: plan.Namespace.ValueString(), Kind: plan.Kind.ValueString(),
	})
	// if err != nil {
	// 	resp.Diagnostics.AddError(
	// 		"Error getting source",
	// 		"Could not get source, unexpected error: "+err.Error(),
	// 	)
	// 	// return
	// }

	diags = mapSourceToModel(&plan, clusterID, source)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

}

func (r *sourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SourceResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID, err := getClusterID(ctx, r.client, state.ClusterName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error getting cluster ID",
			"Could not get cluster ID, unexpected error: "+err.Error(),
		)
		return
	}
	tflog.Trace(ctx, "Name: "+state.Name.ValueString())
	source, err := r.client.GetSource(ctx, clusterID, odigosclient.SourceId{
		Name: state.Name.ValueString(), Namespace: state.Namespace.ValueString(), Kind: state.Kind.ValueString(),
	})

	// if err != nil {
	// 	resp.State.RemoveResource(ctx)
	// 	resp.Diagnostics.AddError(
	// 		"Error getting source",
	// 		"Could not get source, unexpected error: "+err.Error(),
	// 	)
	// 	return
	// }

	if state.ClusterID != types.StringValue(clusterID) || source.Name == "" || source.Namespace == "" || source.Kind == "" {
		resp.Diagnostics.AddWarning(
			"Source No Longer Exists",
			"The source was not found or has been removed. It will be removed from the Terraform state.",
		)
		resp.State.RemoveResource(ctx)
		return
	}
	tflog.Trace(ctx, "Namespace: "+source.Namespace)

	diags = mapSourceToModel(&state, clusterID, source)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *sourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Not needed since all fields are RequiresReplace or computed
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *sourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SourceResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	clusterID, err := getClusterID(ctx, r.client, state.ClusterName.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error getting cluster ID",
			"Could not get cluster ID, unexpected error: "+err.Error(),
		)
		return
	}

	_, err = r.client.PersistSources(ctx, clusterID, []odigosclient.SourceInput{
		{
			Name:              state.Name.ValueString(),
			Namespace:         state.Namespace.ValueString(),
			Kind:              state.Kind.ValueString(),
			CurrentStreamName: state.CurrentStreamName.ValueString(),
			Selected:          false,
		},
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting source",
			"Could not delete source, unexpected error: "+err.Error(),
		)
		return
	}
}

// mapSourceToModel maps the API source response and clusterID onto the given SourceResourceModel.
// It returns diagnostics if any conversion errors occur.
func mapSourceToModel(model *SourceResourceModel, clusterID string, source odigosclient.Source) diag.Diagnostics {
	var diags diag.Diagnostics

	model.ClusterID = types.StringValue(clusterID)
	model.OtelServiceName = types.StringValue(source.OtelServiceName)

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
		containerElems[i] = types.ObjectValueMust(
			containerObjectType.AttrTypes,
			map[string]attr.Value{
				"container_name":          types.StringValue(container.ContainerName),
				"language":                types.StringValue(container.Language),
				"runtime_version":         types.StringValue(container.RuntimeVersion),
				"overridden":              types.BoolValue(container.Overriden),
				"instrumented":            types.BoolValue(container.Instrumented),
				"instrumentation_message": types.StringValue(container.InstrumentationMessage),
				"otel_distro_name":        types.StringValue(container.OtelDistroName),
			},
		)
	}
	var containerDiags diag.Diagnostics
	model.Containers, containerDiags = types.ListValue(containerObjectType, containerElems)
	diags.Append(containerDiags...)
	if diags.HasError() {
		return diags
	}

	conditionElems := make([]attr.Value, len(source.Conditions))
	for i, condition := range source.Conditions {
		conditionElems[i] = types.ObjectValueMust(
			conditionObjectType.AttrTypes,
			map[string]attr.Value{
				"status":               types.StringValue(condition.Status),
				"type":                 types.StringValue(condition.Type),
				"reason":               types.StringValue(condition.Reason),
				"message":              types.StringValue(condition.Message),
				"last_transition_time": types.StringValue(condition.LastTransitionTime.Format(time.RFC3339)),
			},
		)
	}
	var conditionDiags diag.Diagnostics
	model.Conditions, conditionDiags = types.ListValue(conditionObjectType, conditionElems)
	diags.Append(conditionDiags...)
	if diags.HasError() {
		return diags
	}

	return diags
}
