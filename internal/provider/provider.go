package provider

import (
	"context"
	"os"
	"sync"
	"terraform-provider-odigos/pkgs/logger"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	odigosclient "github.com/odigos-io/odigos-central-client"
	odigosLogger "github.com/odigos-io/odigos-central-client/logger"
)

var _ provider.Provider = &odigosProvider{}

// cachedOdigosClient wraps OdigosClient and caches GetComputePlatforms for the duration of the Terraform run using sync.Once.
type cachedOdigosClient struct {
	client        *odigosclient.OdigosClient
	platformsOnce sync.Once
	platforms     []odigosclient.ComputePlatform
	platformsErr  error
}

func (c *cachedOdigosClient) GetComputePlatforms(ctx context.Context) ([]odigosclient.ComputePlatform, error) {
	c.platformsOnce.Do(func() {
		c.platforms, c.platformsErr = c.client.GetComputePlatforms(ctx)
	})
	return c.platforms, c.platformsErr
}

func (c *cachedOdigosClient) GetSource(ctx context.Context, proxyID string, sourceID odigosclient.SourceId) (odigosclient.Source, error) {
	return c.client.GetSource(ctx, proxyID, sourceID)
}

func (c *cachedOdigosClient) PersistSources(ctx context.Context, proxyID string, sources []odigosclient.SourceInput) (bool, error) {
	return c.client.PersistSources(ctx, proxyID, sources)
}

func (c *cachedOdigosClient) GetNamespace(ctx context.Context, proxyID string, namespaceName string) (odigosclient.K8sActualNamespaceDetail, error) {
	return c.client.GetNamespace(ctx, proxyID, namespaceName)
}

func (c *cachedOdigosClient) PersistNamespaceSources(ctx context.Context, proxyID string, namespaces []odigosclient.NamespaceInput) (bool, error) {
	return c.client.PersistNamespaceSources(ctx, proxyID, namespaces)
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &odigosProvider{
			version: version,
		}
	}
}

type odigosProvider struct {
	version string
}

type odigosProviderModel struct {
	Hostname types.String `tfsdk:"hostname"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Insecure types.Bool   `tfsdk:"insecure"`
}

func (p *odigosProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "odigos"
	resp.Version = p.version
}

func (p *odigosProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"hostname": schema.StringAttribute{
				Optional:    true,
				Description: "The Odigos API hostname. Can also be set via ODIGOS_HOSTNAME.",
			},
			"username": schema.StringAttribute{
				Optional:    true,
				Description: "The Odigos API username. Can also be set via ODIGOS_USERNAME.",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "The Odigos API password. Can also be set via ODIGOS_PASSWORD.",
			},
			"insecure": schema.BoolAttribute{
				Optional:    true,
				Description: "Whether to skip TLS verification when connecting to the Odigos API.",
			},
		},
	}
}

func (p *odigosProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config odigosProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.Hostname.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("hostname"),
			"Unknown Odigos API Hostname",
			"The provider cannot create the Odigos API client as there is an unknown configuration value for the Odigos API hostname. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the ODIGOS_HOSTNAME environment variable.",
		)
	}

	if config.Username.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("username"),
			"Unknown Odigos API Username",
			"The provider cannot create the Odigos API client as there is an unknown configuration value for the Odigos API username. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the ODIGOS_USERNAME environment variable.",
		)
	}

	if config.Password.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("password"),
			"Unknown Odigos API Password",
			"The provider cannot create the Odigos API client as there is an unknown configuration value for the Odigos API password. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the ODIGOS_PASSWORD environment variable.",
		)
	}

	if config.Insecure.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("insecure"),
			"Unknown Odigos API Insecure Setting",
			"The provider cannot create the Odigos API client as there is an unknown configuration value for the insecure TLS setting. "+
				"Either target apply the source of the value first or set the value statically in the configuration.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	hostname := os.Getenv("ODIGOS_HOSTNAME")
	username := os.Getenv("ODIGOS_USERNAME")
	password := os.Getenv("ODIGOS_PASSWORD")

	if !config.Hostname.IsNull() {
		hostname = config.Hostname.ValueString()
	}

	if !config.Username.IsNull() {
		username = config.Username.ValueString()
	}

	if !config.Password.IsNull() {
		password = config.Password.ValueString()
	}

	insecure := false
	if !config.Insecure.IsNull() {
		insecure = config.Insecure.ValueBool()
	}

	if hostname == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("hostname"),
			"Missing Odigos API Hostname",
			"The provider cannot create the Odigos API client as there is a missing or empty value for the Odigos API hostname. "+
				"Set the hostname value in the configuration or use the ODIGOS_HOSTNAME environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if username == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("username"),
			"Missing Odigos API Username",
			"The provider cannot create the Odigos API client as there is a missing or empty value for the Odigos API username. "+
				"Set the username value in the configuration or use the ODIGOS_USERNAME environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if password == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("password"),
			"Missing Odigos API Password",
			"The provider cannot create the Odigos API client as there is a missing or empty value for the Odigos API password. "+
				"Set the password value in the configuration or use the ODIGOS_PASSWORD environment variable. "+
				"If either is already set, ensure the value is not empty.",
		)
	}

	if resp.Diagnostics.HasError() {
		return
	}

	clientConfig := odigosclient.ClientConfig{
		Hostname: hostname,
		Username: username,
		Password: password,
		Insecure: insecure,
	}
	logAdapter := logger.TfLogAdapter{}
	client, err := odigosclient.NewClient(
		ctx,
		clientConfig,
		odigosLogger.WithLogger(&logAdapter))

	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to Create Odigos API Client",
			"An unexpected error occurred when creating the Odigos API client. "+
				"If the error is not clear, please contact the provider developers.\n\n"+
				"Odigos Client Error: "+err.Error(),
		)
		return
	}

	resp.DataSourceData = &cachedOdigosClient{client: client}
	resp.ResourceData = &cachedOdigosClient{client: client}
}

func (p *odigosProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSourceResource,
		NewNamespaceResource,
	}
}

func (p *odigosProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewComputePlatformsDataSource,
	}
}
