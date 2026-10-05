terraform {
  required_providers {
    odigos-central = {
      source = "registry.terraform.io/odigos-io/odigos-central"
    }
  }
}

provider "odigos-central" {
  hostname = "localhost:8081"
  username = var.username
  password = var.password

  # A local Central usually serves a self-signed certificate.
  insecure_skip_verify = true
}

# Select a namespace for instrumentation and set its current data stream.
resource "odigos-central_namespace" "example" {
  cluster_name        = "LocalCluster"
  name                = "kv-mall"
  current_stream_name = "default"
  selected            = true
}

output "namespace" {
  description = "The selected namespace and its computed attributes from the Odigos API."
  value       = odigos-central_namespace.example
}

output "namespace_sources" {
  description = "Sources (workloads) in this namespace."
  value       = odigos-central_namespace.example.sources
}
