provider "odigos-central" {
  hostname = "<hostname>"
  username = "<username>"
  password = "<password>"

  # The provider always connects over HTTPS. Uncomment only when Central
  # presents a self-signed or otherwise untrusted certificate.
  # insecure_skip_verify = true
}
