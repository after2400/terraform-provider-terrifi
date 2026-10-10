---
page_title: "terrifi_port_forward Resource - Terrifi"
subcategory: ""
description: |-
  Manages a port forward (NAT rule) on the UniFi controller.
---

# terrifi_port_forward (Resource)

Manages a port forward (NAT rule) on the UniFi controller. Whether a forward also needs a firewall policy under Zone-Based Firewall depends on the controller version; check your rules after creating one.

## Example Usage

### TCP and UDP to a LAN host

```terraform
resource "terrifi_port_forward" "console" {
  name             = "Game console"
  destination_port = "3074"
  forward_ip       = "192.168.1.50"
  forward_port     = "3074"
}
```

### UDP only, restricted to a source network

```terraform
resource "terrifi_port_forward" "remote_play" {
  name             = "Remote play"
  protocol         = "udp"
  destination_port = "9002"
  forward_ip       = "192.168.1.50"
  forward_port     = "9002"
  source_ip        = "203.0.113.0/24"
}
```

## Schema

### Required

- `name` (String) — A name for the rule (1-128 characters).
- `destination_port` (String) — External port, a range (`3074-3080`) or a comma-separated list of either (`80,443,8000-8010`).
- `forward_ip` (String) — LAN IPv4 address the traffic is forwarded to.
- `forward_port` (String) — Port, range or comma-separated list on the forward target.

### Optional

- `enabled` (Boolean) — Whether the rule is enabled. Defaults to `true`.
- `protocol` (String) — Protocol: `tcp_udp`, `tcp` or `udp`. Defaults to `tcp_udp`.
- `interface` (String) — WAN interface the rule listens on: `wan`, `wan2` to `wan9`, `both` or `all`. Defaults to `wan`.
- `destination_ip` (String) — WAN-side destination IP the rule matches, or `any`. Defaults to `any`.
- `source_ip` (String) — Restrict the rule to this source: an IP, an `a.b.c.d-e.f.g.h` range or a CIDR, optionally prefixed with `!` to negate. Omit to allow any source. Cannot be combined with `source_firewall_group_id`.
- `source_firewall_group_id` (String) — Restrict the rule to sources in this firewall group (`terrifi_firewall_group.<name>.id`). Cannot be combined with `source_ip`.
- `log` (Boolean) — Whether to log matching traffic. Defaults to `false`.
- `site` (String) — The site to associate the port forward with. Defaults to the provider site. Changing this forces a new resource.

### Read-Only

- `id` (String) — The ID of the port forward.

## Import

Port forwards can be imported using the rule ID:

```shell
terraform import terrifi_port_forward.console <id>
```

To import a rule from a non-default site, use the `site:id` format:

```shell
terraform import terrifi_port_forward.console <site>:<id>
```

You can also use the [Terrifi CLI](../index.md#cli) to generate import blocks for all port forwards automatically:

```shell
terrifi generate-imports terrifi_port_forward
```
