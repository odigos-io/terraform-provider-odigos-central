terraform {
  required_providers {
    odigos-central = {
      source = "registry.terraform.io/odigos-io/odigos-central"
    }
  }
}

provider "odigos-central" {
  hostname = "odigos-central.example.com:8081"
  username = var.username
  password = var.password
}

resource "odigos-central_source" "instrument" {
  cluster_name        = "my-cluster"
  name                = "my-deployment"
  kind                = "Deployment"
  namespace           = "my-namespace"
  current_stream_name = "default"
  selected            = true
}

output "resource_central_backend" {
  value = odigos-central_source.instrument
}
