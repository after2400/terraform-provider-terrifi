# Example: forward a game port to a LAN host.
#
# Build the provider and point Terraform at it with dev_overrides, then set
# UNIFI_API and UNIFI_API_KEY (see the dns_record example for the full setup).

terraform {
  required_providers {
    terrifi = {
      source = "alexklibisz/terrifi"
    }
  }
}

# TCP and UDP 3074 from the WAN to a console on the LAN.
resource "terrifi_port_forward" "console" {
  name             = "Game console"
  destination_port = "3074"
  forward_ip       = "192.168.1.50"
  forward_port     = "3074"
}

# UDP only, on a different WAN port, restricted to one source network.
resource "terrifi_port_forward" "remote_play" {
  name             = "Remote play"
  protocol         = "udp"
  destination_port = "9002"
  forward_ip       = "192.168.1.50"
  forward_port     = "9002"
  source_ip        = "203.0.113.0/24"
}
