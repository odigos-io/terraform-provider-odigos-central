terraform {
  required_providers {
    odigos = {
      source = "registry.terraform.io/odigos/odigos"
    }
  }
}

provider "odigos" {
  hostname = "localhost:8081"
  username = var.username
  password = var.password
  insecure = true
}

# Select a namespace for instrumentation and set its current data stream.
resource "odigos_namespace" "example" {
  cluster_name        = "LocalCluster"
  name                = "kv-mall"
  current_stream_name = "default"
}

output "namespace" {
  description = "The selected namespace and its computed attributes from the Odigos API."
  value       = odigos_namespace.example
}

output "namespace_sources" {
  description = "Sources (workloads) in this namespace."
  value       = odigos_namespace.example.sources
}
