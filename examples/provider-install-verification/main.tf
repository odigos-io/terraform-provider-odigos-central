terraform {
  required_providers {
    odigos = {
      source = "registry.terraform.io/odigos/odigos"
    }
  }
}

provider "odigos" {
  hostname = "localhost:8081"
  insecure = true

  # Credentials are read from ODIGOS_USERNAME and ODIGOS_PASSWORD.
}

data "odigos_compute_platforms" "example" {}

output "odigos_compute_platforms" {
  value = data.odigos_compute_platforms.example
}