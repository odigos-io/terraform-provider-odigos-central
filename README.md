# Terraform Provider for Odigos Central

Manages [Odigos Central](https://odigos.io): connected clusters, namespace selection, and workload instrumentation.

Registry source: `odigos-io/odigos-central`.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0
- [Go](https://go.dev/doc/install) >= 1.25

## Building The Provider

1. Clone the repository
2. Enter the repository directory
3. Build the provider using the Go `install` command:

```shell
go install
```

## Using the Provider

The provider talks to the Odigos Central API over HTTPS.

```terraform
terraform {
  required_providers {
    odigos-central = {
      source = "odigos-io/odigos-central"
    }
  }
}

provider "odigos-central" {
  hostname = "odigos-central.example.com:8081"
  username = var.username
  password = var.password

  # Uncomment only when Central presents a self-signed or untrusted certificate.
  # insecure_skip_verify = true
}
```

Arguments can also be set with `ODIGOS_HOSTNAME`, `ODIGOS_USERNAME`, `ODIGOS_PASSWORD`, and `ODIGOS_INSECURE_SKIP_VERIFY`.

Resources: `odigos-central_namespace`, `odigos-central_source`. Data source: `odigos-central_compute_platforms`. See `docs/` and `examples/` for details.