resource "odigos-central_namespace" "example" {
  cluster_name        = "my-cluster"
  name                = "my-namespace"
  current_stream_name = "default"
}
