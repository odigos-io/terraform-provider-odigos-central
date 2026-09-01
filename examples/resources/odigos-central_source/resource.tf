resource "odigos-central_source" "example" {
  cluster_name        = "my-cluster"
  name                = "my-deployment"
  kind                = "Deployment"
  namespace           = "my-namespace"
  current_stream_name = "default"
}
