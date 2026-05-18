package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ datasource.DataSource              = &computePlatformsDataSource{}
	_ datasource.DataSourceWithConfigure = &computePlatformsDataSource{}
)

func NewComputePlatformsDataSource() datasource.DataSource {
	return &computePlatformsDataSource{}
}

type computePlatformsDataSource struct {
	client *cachedOdigosClient
}

type computePlatformsDataSourceModel struct {
	ComputePlatforms []computePlatformsModel `tfsdk:"compute_platforms"`
}

type computePlatformsModel struct {
	Id            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	OdigosVersion types.String `tfsdk:"odigos_version"`
	Type          types.String `tfsdk:"type"`
	Status        types.String `tfsdk:"status"`
	ConnectedAt   types.Int64  `tfsdk:"connected_at"`
	LastSeenAt    types.Int64  `tfsdk:"last_seen_at"`
	Users         types.List   `tfsdk:"users"`
	Teams         types.List   `tfsdk:"teams"`
	TypeName      types.String `tfsdk:"type_name"`
}

func (d *computePlatformsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compute_platforms"
}

func (d *computePlatformsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"compute_platforms": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Computed:    true,
							Description: "The id of the compute platform",
						},
						"name": schema.StringAttribute{
							Computed:    true,
							Description: "The name of the compute platform",
						},
						"odigos_version": schema.StringAttribute{
							Computed:    true,
							Description: "The odigos version of the compute platform",
						},
						"type": schema.StringAttribute{
							Computed:    true,
							Description: "The type of the compute platform",
						},
						"status": schema.StringAttribute{
							Computed:    true,
							Description: "The status of the compute platform",
						},
						"connected_at": schema.Int64Attribute{
							Computed:    true,
							Description: "The connected at time of the compute platform",
						},
						"last_seen_at": schema.Int64Attribute{
							Computed:    true,
							Description: "The last seen at time of the compute platform",
						},
						"users": schema.ListAttribute{
							Computed:    true,
							Description: "The users of the compute platform",
							ElementType: types.StringType,
						},
						"teams": schema.ListAttribute{
							Computed:    true,
							Description: "The teams of the compute platform",
							ElementType: types.StringType,
						},
						"type_name": schema.StringAttribute{
							Computed:    true,
							Description: "The type name of the compute platform",
						},
					},
				},
			},
		},
	}
}

func (d *computePlatformsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*cachedOdigosClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *cachedOdigosClient, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	d.client = client
}

func (d *computePlatformsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state computePlatformsDataSourceModel

	platforms, err := d.client.GetComputePlatforms(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Get Compute Platforms",
			"An unexpected error occurred when getting the compute platforms. "+
				"If the error is not clear, please contact the provider developers.\n\n"+
				"Odigos Client Error: "+err.Error(),
		)
		return
	}

	for _, cluster := range platforms {
		users, usersDiags := anySliceToStrings(cluster.Users)
		resp.Diagnostics.Append(usersDiags...)
		teams, teamsDiags := anySliceToStrings(cluster.Teams)
		resp.Diagnostics.Append(teamsDiags...)

		userElems := make([]attr.Value, len(users))
		for i, u := range users {
			userElems[i] = types.StringValue(u)
		}
		usersList, listDiags := types.ListValue(types.StringType, userElems)
		resp.Diagnostics.Append(listDiags...)

		teamElems := make([]attr.Value, len(teams))
		for i, t := range teams {
			teamElems[i] = types.StringValue(t)
		}
		teamsList, listDiags := types.ListValue(types.StringType, teamElems)
		resp.Diagnostics.Append(listDiags...)

		state.ComputePlatforms = append(state.ComputePlatforms, computePlatformsModel{
			Id:            types.StringValue(cluster.ID),
			Name:          types.StringValue(cluster.Name),
			OdigosVersion: types.StringValue(cluster.OdigosVersion),
			Type:          types.StringValue(cluster.Type),
			Status:        types.StringValue(cluster.Status),
			ConnectedAt:   types.Int64Value(cluster.ConnectedAt),
			LastSeenAt:    types.Int64Value(cluster.LastSeenAt),
			Users:         usersList,
			Teams:         teamsList,
			TypeName:      types.StringValue(cluster.Typename),
		})
	}

	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
}

// anySliceToStrings converts []any from the API to []string, skipping non-string elements.
// It returns a diagnostic warning when any element is skipped.
func anySliceToStrings(in []any) ([]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if in == nil {
		return nil, diags
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if s, ok := v.(string); ok {
			out = append(out, s)
		} else {
			diags.AddWarning(
				"Compute Platform List Element Skipped",
				fmt.Sprintf("Expected string in users/teams list but got %T; skipping non-string element.", v),
			)
		}
	}
	return out, diags
}
