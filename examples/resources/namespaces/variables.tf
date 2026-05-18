variable "username" {
  description = "Username for the Odigos provider"
  type        = string
}

variable "password" {
  description = "Password for the Odigos provider"
  type        = string
  sensitive   = true
}

