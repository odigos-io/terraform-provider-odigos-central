package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
	odigosclient "github.com/odigos-io/odigos-central-client"
)

var (
	_ resource.Resource              = &namespaceResource{}
	_ resource.ResourceWithConfigure = &namespaceResource{}
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
		Attributes: map[string]schema.Attribute{
			"cluster_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the cluster (compute platform).",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the namespace.",
			},
			"current_stream_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the data stream to use when persisting namespace selection.",
			},
			"selected": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the namespace is selected.",
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

func (r *namespaceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan NamespaceResourceModel
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

	namespaceToPersist := []odigosclient.NamespaceInput{
		{
			Namespace:         plan.Name.ValueString(),
			CurrentStreamName: plan.CurrentStreamName.ValueString(),
			Selected:          true,
		},
	}

	success, err := r.client.PersistNamespaceSources(ctx, clusterID, namespaceToPersist)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error persisting namespace sources",
			"Could not persist namespace sources, unexpected error: "+err.Error(),
		)
		return
	}

	if !success {
		resp.Diagnostics.AddError(
			"Error persisting namespace sources",
			"The API returned success=false when persisting namespace sources.",
		)
		return
	}

	detail, err := r.client.GetNamespace(ctx, clusterID, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading namespace after create",
			"Could not get namespace, unexpected error: "+err.Error(),
		)
		return
	}

	state := namespaceDetailToModel(plan.ClusterName, plan.CurrentStreamName, detail)
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *namespaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state NamespaceResourceModel
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
	tflog.Trace(ctx, "Namespace: "+state.Name.ValueString())

	detail, err := r.client.GetNamespace(ctx, clusterID, state.Name.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
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

	newState := namespaceDetailToModel(state.ClusterName, state.CurrentStreamName, detail)
	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *namespaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *namespaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state NamespaceResourceModel
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

	_, err = r.client.PersistNamespaceSources(ctx, clusterID, []odigosclient.NamespaceInput{
		{
			Namespace:         state.Name.ValueString(),
			CurrentStreamName: state.CurrentStreamName.ValueString(),
			Selected:          false,
		},
	})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting namespace selection",
			"Could not persist namespace sources, unexpected error: "+err.Error(),
		)
		return
	}
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
func namespaceDetailToModel(clusterName, currentStreamName types.String, detail odigosclient.K8sActualNamespaceDetail) NamespaceResourceModel {
	model := NamespaceResourceModel{
		ClusterName:       clusterName,
		CurrentStreamName: currentStreamName,
		Name:              types.StringValue(detail.Name),
		Selected:          types.BoolValue(detail.Selected),
	}

	model.DataStreamNames, _ = types.ListValueFrom(context.Background(), types.StringType, detail.DataStreamNames)

	sourceElems := make([]attr.Value, len(detail.Sources))
	for i, s := range detail.Sources {
		dsList, _ := types.ListValueFrom(context.Background(), types.StringType, s.DataStreamNames)
		sourceElems[i], _ = types.ObjectValue(namespaceSourceObjectType.AttrTypes, map[string]attr.Value{
			"namespace":           types.StringValue(s.Namespace),
			"kind":                types.StringValue(s.Kind),
			"name":                types.StringValue(s.Name),
			"data_stream_names":   dsList,
			"selected":            types.BoolValue(s.Selected),
			"number_of_instances": types.Int64Value(int64(s.NumberOfInstances)),
		})
	}
	model.Sources, _ = types.ListValue(namespaceSourceObjectType, sourceElems)

	return model
}
