terraform {
  required_providers {
    odigos = {
      source = "registry.terraform.io/odigos/odigos"
    }
  }
}

provider "odigos" {
  hostname = "odigos-central.example.com:8081"
  username = var.username
  password = var.password
}

resource "odigos_source" "instrument" {
  cluster_name        = "my-cluster"
  name                = "my-deployment"
  kind                = "Deployment"
  namespace           = "my-namespace"
  current_stream_name = "default"
}

output "resource_central_backend" {
  value = odigos_source.instrument
}
