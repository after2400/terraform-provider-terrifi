package generate

import (
	"github.com/ubiquiti-community/go-unifi/unifi"
)

// PortForwardBlocks generates import + resource blocks for port forwards.
// Attributes equal to the resource defaults are left out.
func PortForwardBlocks(forwards []unifi.PortForward) []ResourceBlock {
	blocks := make([]ResourceBlock, 0, len(forwards))
	for _, f := range forwards {
		block := ResourceBlock{
			ResourceType: "terrifi_port_forward",
			ResourceName: ToTerraformName(f.Name),
			ImportID:     f.ID,
		}

		add := func(key, value string) { block.Attributes = append(block.Attributes, Attr{Key: key, Value: value}) }

		add("name", HCLString(f.Name))
		add("destination_port", HCLString(f.DstPort))
		add("forward_ip", HCLString(f.Fwd))
		add("forward_port", HCLString(f.FwdPort))

		if f.Proto != "" && f.Proto != "tcp_udp" {
			add("protocol", HCLString(f.Proto))
		}
		if f.PfwdInterface != "" && f.PfwdInterface != "wan" {
			add("interface", HCLString(f.PfwdInterface))
		}
		if f.DestinationIP != "" && f.DestinationIP != "any" {
			add("destination_ip", HCLString(f.DestinationIP))
		}
		if !f.Enabled {
			add("enabled", HCLBool(false))
		}
		if f.Log {
			add("log", HCLBool(true))
		}
		if f.SrcLimitingEnabled {
			switch f.SrcLimitingType {
			case "ip":
				if f.Src != "" && f.Src != "any" {
					add("source_ip", HCLString(f.Src))
				}
			case "firewall_group":
				if f.SrcFirewallGroupID != "" {
					add("source_firewall_group_id", HCLString(f.SrcFirewallGroupID))
				}
			}
		}

		blocks = append(blocks, block)
	}
	DeduplicateNames(blocks)
	return blocks
}
