// Copyright (c) Odigos
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"terraform-provider-odigos-central/pkgs/logger"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/odigos-io/odigos-central-client/central"
	odigosLogger "github.com/odigos-io/odigos-central-client/logger"
	odigostypes "github.com/odigos-io/odigos-central-client/types"
)

var _ provider.Provider = &odigosProvider{}

// cachedOdigosClient wraps a *central.CentralClient with provider-friendly
// caching:
//
//   - GetComputePlatforms is already cached inside the central client; this
//     wrapper exposes it transparently so existing call sites keep working.
//
//   - ProxyByName is cached per cluster name (sync.Map) so multiple resources
//     targeting the same cluster don't re-resolve the platform on every call.
//
//   - Proxy resolution returns Terraform diag.Diagnostics rather than raw
//     errors so each resource can decide whether the situation is fatal
//     (cluster not found, GraphQL error) or a soft warning (proxy version
//     too old, unsupported platform). Read keeps prior state for that
//     warning; Create, Update, and Delete error so Terraform does not
//     pretend the remote object changed.
type cachedOdigosClient struct {
	client  *central.CentralClient
	proxies sync.Map

	// persistLocks serializes create and destroy of resources that share one
	// Odigos Source CRD (same cluster + workload, or same cluster + namespace).
	persistMu    sync.Mutex
	persistLocks map[string]*sync.Mutex
}

func sourcePersistKey(cluster, namespace, kind, name string) string {
	return "source\x00" + cluster + "\x00" + namespace + "\x00" + kind + "\x00" + name
}

func namespacePersistKey(cluster, name string) string {
	return "namespace\x00" + cluster + "\x00" + name
}

// lockPersist returns an unlock function for key. Callers must defer the result.
// Separate keys run concurrently; the same key waits.
func (c *cachedOdigosClient) lockPersist(key string) func() {
	if c == nil {
		return func() {}
	}
	c.persistMu.Lock()
	if c.persistLocks == nil {
		c.persistLocks = make(map[string]*sync.Mutex)
	}
	m, ok := c.persistLocks[key]
	if !ok {
		m = &sync.Mutex{}
		c.persistLocks[key] = m
	}
	c.persistMu.Unlock()
	m.Lock()
	return m.Unlock
}

// existingMembershipStream is the data stream an object is already on.
// GetSource / GetNamespace can include extra names (including the stream this
// Create is requesting). Membership is a single stream, so skip plannedStream
// when another name is present.
func existingMembershipStream(streams []string, plannedStream string) string {
	for _, s := range streams {
		if s != plannedStream {
			return s
		}
	}
	return streams[0]
}

// sourceNotYetCreated reports whether GetSource failed because there is no
// InstrumentationConfig CRD. That is the first-create case, and also what
// Odigos returns after selected=false removes the last stream label (the
// Source CRD is deleted). It is not a transport or GraphQL failure.
func sourceNotYetCreated(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") &&
		(strings.Contains(msg, "InstrumentationConfig") ||
			strings.Contains(msg, "instrumentationconfigs.odigos.io"))
}

func annotateDuplicateSourceError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "expected to get 1 source") {
		return msg + ". Delete extra Source CRDs for this workload and retry."
	}
	return msg
}

func (c *cachedOdigosClient) GetComputePlatforms(ctx context.Context) ([]odigostypes.ComputePlatform, error) {
	return c.client.GetComputePlatforms(ctx)
}

// ProxyByName resolves a cluster name to a *central.ProxyClient. If the
// cluster doesn't exist, an error diagnostic is returned. If the cluster is
// unsupported (too old, unknown platform), a *warning* diagnostic is
// returned and the *ProxyClient is nil. Callers must not treat that as
// "resource gone": Read should keep prior state; mutating operations should error.
func (c *cachedOdigosClient) ProxyByName(ctx context.Context, name string) (*central.ProxyClient, diag.Diagnostics) {
	var diags diag.Diagnostics

	if v, ok := c.proxies.Load(name); ok {
		// The pointer must be extracted before testing for nil: the negative
		// answer cached below is a typed nil, which is never == nil once
		// wrapped in an interface.
		cachedProxy, _ := v.(*central.ProxyClient)
		if cachedProxy == nil {
			diags.AddWarning(
				"Skipping Unsupported Cluster",
				"Cluster "+name+" was previously detected as unsupported by this provider; subsequent operations will be skipped for this Terraform run.",
			)
			return nil, diags
		}
		return cachedProxy, diags
	}

	proxy, err := c.client.ProxyByName(ctx, name)
	if err == nil {
		c.proxies.Store(name, proxy)
		return proxy, diags
	}

	if central.IsUnsupported(err) {
		// Per-proxy soft fail: cache the negative answer so we don't keep
		// re-resolving and emit only one warning per cluster per run.
		c.proxies.Store(name, (*central.ProxyClient)(nil))
		diags.AddWarning(
			"Cluster Not Supported By Provider",
			"Skipping cluster "+name+": "+err.Error(),
		)
		return nil, diags
	}

	diags.AddError("Error Resolving Cluster", err.Error())
	return nil, diags
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
	Hostname           types.String `tfsdk:"hostname"`
	Username           types.String `tfsdk:"username"`
	Password           types.String `tfsdk:"password"`
	InsecureSkipVerify types.Bool   `tfsdk:"insecure_skip_verify"`
}

func (p *odigosProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "odigos-central"
	resp.Version = p.version
}

func (p *odigosProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Odigos Central: connected clusters, namespace selection, and workload " +
			"instrumentation. The provider always connects to the Central API over HTTPS. Set `hostname`, " +
			"`username`, and `password` here or via `ODIGOS_HOSTNAME`, `ODIGOS_USERNAME`, and `ODIGOS_PASSWORD`. " +
			"`insecure_skip_verify` (or `ODIGOS_INSECURE_SKIP_VERIFY`) disables TLS certificate and hostname " +
			"verification and should be used only with a self-signed or otherwise untrusted Central certificate.",
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
			"insecure_skip_verify": schema.BoolAttribute{
				Optional: true,
				Description: "Skip verification of the Odigos API's TLS certificate and hostname. The connection stays " +
					"encrypted but becomes vulnerable to man-in-the-middle attacks, so enable this only when Central " +
					"presents a self-signed or otherwise untrusted certificate, such as on a dev or test cluster. " +
					"Defaults to false. Can also be set via ODIGOS_INSECURE_SKIP_VERIFY.",
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

	if config.InsecureSkipVerify.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("insecure_skip_verify"),
			"Unknown Odigos API TLS Verification Setting",
			"The provider cannot create the Odigos API client as there is an unknown configuration value for insecure_skip_verify. "+
				"Either target apply the source of the value first, set the value statically in the configuration, or use the ODIGOS_INSECURE_SKIP_VERIFY environment variable.",
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

	insecureSkipVerify := false
	if env := os.Getenv("ODIGOS_INSECURE_SKIP_VERIFY"); env != "" {
		parsed, err := strconv.ParseBool(env)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("insecure_skip_verify"),
				"Invalid ODIGOS_INSECURE_SKIP_VERIFY Value",
				"The ODIGOS_INSECURE_SKIP_VERIFY environment variable must be a boolean such as \"true\" or \"false\", but was "+strconv.Quote(env)+".",
			)
		} else {
			insecureSkipVerify = parsed
		}
	}
	if !config.InsecureSkipVerify.IsNull() {
		insecureSkipVerify = config.InsecureSkipVerify.ValueBool()
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

	// ClientConfig.Insecure is deliberately left unset. It downgrades the
	// connection to plain HTTP, which would put the password on the wire in
	// cleartext, so the provider always speaks HTTPS to Central.
	clientConfig := central.ClientConfig{
		Hostname:           hostname,
		Username:           username,
		Password:           password,
		InsecureSkipVerify: insecureSkipVerify,
	}

	logAdapter := logger.TfLogAdapter{}
	client, err := central.NewClient(ctx, clientConfig, odigosLogger.WithLogger(&logAdapter))
	if err != nil {
		// Hard-fail at Configure for *Central* version mismatches: the user
		// must upgrade Central (or downgrade the provider) before any
		// resources can be managed.
		var ucv *central.UnsupportedCentralVersionError
		if errors.As(err, &ucv) {
			resp.Diagnostics.AddError(
				"Odigos Central Version Not Supported",
				"This provider requires Odigos Central "+ucv.Min.String()+" or newer, but the connected Central server reports "+ucv.Got.String()+". "+
					"Either upgrade the Central deployment or pin to an older version of the provider.",
			)
			return
		}

		resp.Diagnostics.AddError(
			"Unable to Create Odigos API Client",
			"An unexpected error occurred when creating the Odigos API client. "+
				"If the error is not clear, please contact the provider developers.\n\n"+
				"Odigos Client Error: "+err.Error(),
		)
		return
	}

	cached := &cachedOdigosClient{client: client}
	resp.DataSourceData = cached
	resp.ResourceData = cached
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
