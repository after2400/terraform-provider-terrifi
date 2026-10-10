package provider

// TODO(go-unifi): This file contains two workarounds for go-unifi SDK bugs
// affecting device CRUD.
//
// 1. UpdateDevice diff mechanism. The SDK's UpdateDevice (1) re-fetches the
// device by MAC internally, (2) diffs against the target using JSON
// marshal/unmarshal, and (3) sends only changed fields. When the re-fetched
// device differs from the target in unexpected ways (e.g., stat counters that
// changed between reads, or structural differences between list vs.
// single-device endpoints), the diff includes noise that confuses the
// controller, causing it to return empty results (our "not found: type="
// error). This workaround sends a minimal PUT payload containing only the
// fields we actually manage, avoiding the fragile diff mechanism entirely.
// Fix needed in SDK: UpdateDevice's diff approach should be more robust, or
// provide a way to do a simple field-level PUT without the diff.
//
// 2. DeviceRadioTable.TxPower and DeviceRadioTable.Channel unmarshal. The SDK
// declares both as Go strings (json tags `tx_power` and `channel`), but the
// controller emits them as JSON numbers for some devices (e.g. an integer dBm
// tx_power value, or a numeric channel on devices like the Dream Router 7 and
// USW Flex XG), so the SDK's UnmarshalJSON for DeviceRadioTable fails with:
// "unable to unmarshal alias: json: cannot unmarshal number into Go struct
// field .Alias.tx_power of type string" (or .Alias.channel). This blocks
// every Read on affected sites. We bypass GetDevice/GetDeviceByMAC/
// ListDevice in the SDK and call the v1 stat/device endpoints ourselves,
// pre-processing the raw JSON to wrap numeric tx_power and channel values in
// quotes before unmarshaling into unifi.Device.
// Fix needed in SDK: TxPower and Channel should accept either a string or a
// number on the wire (e.g. via a custom UnmarshalJSON that uses json.Number).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/ubiquiti-community/go-unifi/unifi"
)

// deviceUpdatePayload contains only the fields the device resource manages.
// By sending only these fields, we avoid the SDK's diff mechanism which
// produces spurious changes from stat counters and other runtime fields.
type deviceUpdatePayload struct {
	Name                       string                      `json:"name"`
	LedOverride                string                      `json:"led_override,omitempty"`
	LedOverrideColor           string                      `json:"led_override_color,omitempty"`
	LedOverrideColorBrightness *int64                      `json:"led_override_color_brightness,omitempty"`
	OutdoorModeOverride        string                      `json:"outdoor_mode_override,omitempty"`
	Locked                     bool                        `json:"locked"`
	Disabled                   bool                        `json:"disabled"`
	SnmpContact                string                      `json:"snmp_contact,omitempty"`
	SnmpLocation               string                      `json:"snmp_location,omitempty"`
	Volume                     *int64                      `json:"volume,omitempty"`
	ConfigNetwork              *deviceConfigNetworkPayload `json:"config_network,omitempty"`
	RadioTable                 []unifi.DeviceRadioTable    `json:"radio_table,omitempty"`
	// PortOverrides is sent as raw JSON blobs rather than the typed
	// unifi.DevicePortOverrides struct. See fetchDevicePortOverridesRaw for
	// why: the typed struct's omitempty tags on every boolean field would
	// silently drop e.g. "autoneg": false on a decode/re-encode round trip.
	PortOverrides []json.RawMessage `json:"port_overrides,omitempty"`
}

type deviceConfigNetworkPayload struct {
	Type    string `json:"type"`
	IP      string `json:"ip,omitempty"`
	Netmask string `json:"netmask,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	DNS1    string `json:"dns1,omitempty"`
	DNS2    string `json:"dns2,omitempty"`
}

// applyPlannedToRadioEntry updates only the user-configured fields in an
// existing DeviceRadioTable entry, preserving all other controller-managed fields.
func applyPlannedToRadioEntry(rt *unifi.DeviceRadioTable, planned deviceRadioSettingsModel) {
	if !planned.Channel.IsNull() && !planned.Channel.IsUnknown() {
		rt.Channel = planned.Channel.ValueString()
	}
	if !planned.ChannelWidth.IsNull() && !planned.ChannelWidth.IsUnknown() {
		v := planned.ChannelWidth.ValueInt64()
		rt.Ht = &v
	}
	if !planned.TransmitPower.IsNull() && !planned.TransmitPower.IsUnknown() {
		rt.TxPower = planned.TransmitPower.ValueString()
	}
	if !planned.TransmitPowerMode.IsNull() && !planned.TransmitPowerMode.IsUnknown() {
		rt.TxPowerMode = planned.TransmitPowerMode.ValueString()
	}
	if !planned.MinRssiEnabled.IsNull() && !planned.MinRssiEnabled.IsUnknown() {
		rt.MinRssiEnabled = planned.MinRssiEnabled.ValueBool()
	}
	if !planned.MinRssi.IsNull() && !planned.MinRssi.IsUnknown() {
		v := planned.MinRssi.ValueInt64()
		rt.MinRssi = &v
	}
}

// applyPlannedToPortOverrideEntry updates only the user-configured fields in
// a raw port_overrides JSON entry (decoded to map[string]any), preserving
// every other key byte-for-byte — including boolean fields the SDK's typed
// unifi.DevicePortOverrides struct would silently drop from a re-marshal via
// omitempty (see fetchDevicePortOverridesRaw). This is the field-level half
// of the two-level merge — see UpdateDevice for the port-level half.
func applyPlannedToPortOverrideEntry(ctx context.Context, entry map[string]any, planned devicePortOverrideModel) {
	if !planned.Forward.IsNull() && !planned.Forward.IsUnknown() {
		entry["forward"] = planned.Forward.ValueString()
	}
	if !planned.OpMode.IsNull() && !planned.OpMode.IsUnknown() {
		entry["op_mode"] = planned.OpMode.ValueString()
	}
	if !planned.NativeNetworkID.IsNull() && !planned.NativeNetworkID.IsUnknown() {
		entry["native_networkconf_id"] = planned.NativeNetworkID.ValueString()
	}
	if !planned.TaggedVLANMgmt.IsNull() && !planned.TaggedVLANMgmt.IsUnknown() {
		entry["tagged_vlan_mgmt"] = planned.TaggedVLANMgmt.ValueString()
	}
	if !planned.PoeMode.IsNull() && !planned.PoeMode.IsUnknown() {
		entry["poe_mode"] = planned.PoeMode.ValueString()
	}
	if !planned.ExcludedNetworkIDs.IsNull() && !planned.ExcludedNetworkIDs.IsUnknown() {
		var ids []string
		planned.ExcludedNetworkIDs.ElementsAs(ctx, &ids, false)
		entry["excluded_networkconf_ids"] = ids
	}
}

// UpdateDevice sends a PUT to the UniFi REST API with only the managed fields.
// This bypasses the SDK's UpdateDevice which uses a fragile diff mechanism.
func (c *Client) UpdateDevice(ctx context.Context, site string, id string, m *deviceResourceModel) error {
	payload := deviceUpdatePayload{}

	if !m.Name.IsNull() && !m.Name.IsUnknown() {
		payload.Name = m.Name.ValueString()
	}

	if !m.LedEnabled.IsNull() && !m.LedEnabled.IsUnknown() {
		if m.LedEnabled.ValueBool() {
			payload.LedOverride = "on"
		} else {
			payload.LedOverride = "off"
		}
	}

	if !m.LedColor.IsNull() && !m.LedColor.IsUnknown() {
		payload.LedOverrideColor = m.LedColor.ValueString()
	}

	if !m.LedBrightness.IsNull() && !m.LedBrightness.IsUnknown() {
		v := m.LedBrightness.ValueInt64()
		payload.LedOverrideColorBrightness = &v
	}

	if !m.OutdoorModeOverride.IsNull() && !m.OutdoorModeOverride.IsUnknown() {
		payload.OutdoorModeOverride = m.OutdoorModeOverride.ValueString()
	}

	if !m.Locked.IsNull() && !m.Locked.IsUnknown() {
		payload.Locked = m.Locked.ValueBool()
	}

	if !m.Disabled.IsNull() && !m.Disabled.IsUnknown() {
		payload.Disabled = m.Disabled.ValueBool()
	}

	if !m.SnmpContact.IsNull() && !m.SnmpContact.IsUnknown() {
		payload.SnmpContact = m.SnmpContact.ValueString()
	}

	if !m.SnmpLocation.IsNull() && !m.SnmpLocation.IsUnknown() {
		payload.SnmpLocation = m.SnmpLocation.ValueString()
	}

	if !m.Volume.IsNull() && !m.Volume.IsUnknown() {
		v := m.Volume.ValueInt64()
		payload.Volume = &v
	}

	if m.ConfigNetwork != nil {
		payload.ConfigNetwork = &deviceConfigNetworkPayload{
			Type:    m.ConfigNetwork.Type.ValueString(),
			IP:      m.ConfigNetwork.IP.ValueString(),
			Netmask: m.ConfigNetwork.Netmask.ValueString(),
			Gateway: m.ConfigNetwork.Gateway.ValueString(),
			DNS1:    m.ConfigNetwork.DNS1.ValueString(),
			DNS2:    m.ConfigNetwork.DNS2.ValueString(),
		}
	}

	plannedByRadio := map[string]*deviceRadioSettingsModel{}
	if m.Radio24 != nil {
		plannedByRadio["ng"] = m.Radio24
	}
	if m.Radio5 != nil {
		plannedByRadio["na"] = m.Radio5
	}
	if m.Radio6 != nil {
		plannedByRadio["6e"] = m.Radio6
	}

	plannedByPort := map[int64]*devicePortOverrideModel{}
	for i := range m.PortOverrides {
		if m.PortOverrides[i].PortIDX.IsNull() || m.PortOverrides[i].PortIDX.IsUnknown() {
			continue
		}
		plannedByPort[m.PortOverrides[i].PortIDX.ValueInt64()] = &m.PortOverrides[i]
	}

	if len(plannedByRadio) > 0 {
		// Read-modify-write: fetch the current device to preserve
		// non-managed radio entries and fields.
		existing, err := c.GetDevice(ctx, site, id)
		if err != nil {
			return fmt.Errorf("reading device for radio merge: %w", err)
		}

		radioTable := make([]unifi.DeviceRadioTable, len(existing.RadioTable))
		copy(radioTable, existing.RadioTable)
		for i := range radioTable {
			if planned, ok := plannedByRadio[radioTable[i].Radio]; ok {
				applyPlannedToRadioEntry(&radioTable[i], *planned)
			}
		}
		payload.RadioTable = radioTable
	}

	if len(plannedByPort) > 0 {
		// Read-modify-write against the raw (undecoded) port_overrides JSON —
		// see fetchDevicePortOverridesRaw for why we avoid the typed SDK
		// struct here. This preserves non-managed ports and non-managed
		// fields on declared ports byte-for-byte (port-level and field-level
		// halves of the merge, respectively — see
		// applyPlannedToPortOverrideEntry for the field-level half).
		rawPorts, err := c.fetchDevicePortOverridesRaw(ctx, site, m.MAC.ValueString())
		if err != nil {
			return fmt.Errorf("reading device port_overrides for merge: %w", err)
		}

		merged := make([]json.RawMessage, 0, len(rawPorts)+len(plannedByPort))
		matched := make(map[int64]bool, len(plannedByPort))

		for i, raw := range rawPorts {
			var entry map[string]any
			if err := json.Unmarshal(raw, &entry); err != nil {
				return fmt.Errorf("decoding port_overrides[%d]: %w", i, err)
			}
			idxFloat, ok := entry["port_idx"].(float64)
			if !ok {
				merged = append(merged, raw) // no port_idx on this entry — leave byte-for-byte untouched
				continue
			}
			idx := int64(idxFloat)
			planned, ok := plannedByPort[idx]
			if !ok {
				merged = append(merged, raw) // undeclared port — leave byte-for-byte untouched
				continue
			}
			applyPlannedToPortOverrideEntry(ctx, entry, *planned)
			b, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("encoding merged port_overrides[%d]: %w", i, err)
			}
			merged = append(merged, b)
			matched[idx] = true
		}

		// A declared port with no existing live entry gets a brand-new entry
		// appended, matching what the controller UI does when you customize a
		// previously-untouched port. port_overrides is a sparse array on the
		// controller — factory-default ports often have no entry at all.
		for idx, planned := range plannedByPort {
			if matched[idx] {
				continue
			}
			entry := map[string]any{"port_idx": idx}
			applyPlannedToPortOverrideEntry(ctx, entry, *planned)
			b, err := json.Marshal(entry)
			if err != nil {
				return fmt.Errorf("encoding new port_overrides entry for port_idx %d: %w", idx, err)
			}
			merged = append(merged, b)
		}

		payload.PortOverrides = merged
	}

	// Use the v1 REST API endpoint for device updates.
	var resp struct {
		Meta struct {
			RC  string `json:"rc"`
			Msg string `json:"msg,omitempty"`
		} `json:"meta"`
		Data []unifi.Device `json:"data"`
	}

	url := fmt.Sprintf("%s%s/api/s/%s/rest/device/%s", c.BaseURL, c.APIPath, site, id)
	err := c.doV2Request(ctx, http.MethodPut, url, payload, &resp)
	if err != nil {
		return err
	}

	if resp.Meta.RC != "ok" {
		return fmt.Errorf("controller returned rc=%s msg=%s", resp.Meta.RC, resp.Meta.Msg)
	}

	return nil
}

// txPowerNumberRE matches a JSON `"tx_power": <number>` pair so we can wrap the
// number in quotes before handing the bytes to the SDK's UnmarshalJSON, which
// only accepts a string. The controller emits the field as a number for some
// devices (notably APs reporting dBm as an integer).
var txPowerNumberRE = regexp.MustCompile(`"tx_power"\s*:\s*(-?\d+(?:\.\d+)?)`)

// channelNumberRE matches a JSON `"channel": <number>` pair so we can wrap the
// number in quotes before handing the bytes to the SDK's UnmarshalJSON, which
// only accepts a string. Some controllers (e.g. Dream Router 7, USW Flex XG)
// emit the field as a number.
var channelNumberRE = regexp.MustCompile(`"channel"\s*:\s*(-?\d+(?:\.\d+)?)`)

// fixRadioTableBytes coerces JSON numeric tx_power and channel values into
// strings so the SDK's DeviceRadioTable.UnmarshalJSON does not fail. See the
// file-level TODO for details.
func fixRadioTableBytes(b []byte) []byte {
	b = txPowerNumberRE.ReplaceAll(b, []byte(`"tx_power":"$1"`))
	b = channelNumberRE.ReplaceAll(b, []byte(`"channel":"$1"`))
	return b
}

// deviceListResponse is the v1 stat/device envelope. We unmarshal into
// json.RawMessage first so we can patch the bytes before decoding into
// unifi.Device. See fixRadioTableBytes.
type deviceListResponse struct {
	Meta struct {
		RC  string `json:"rc"`
		Msg string `json:"msg,omitempty"`
	} `json:"meta"`
	Data []json.RawMessage `json:"data"`
}

// fetchDeviceList performs a GET against the v1 stat/device endpoint, applies
// the radio_table coercions, and decodes each entry into unifi.Device.
func (c *Client) fetchDeviceList(ctx context.Context, site, suffix string) ([]unifi.Device, error) {
	url := fmt.Sprintf("%s%s/api/s/%s/stat/device", c.BaseURL, c.APIPath, site)
	if suffix != "" {
		url += "/" + suffix
	}

	var raw json.RawMessage
	if err := c.doV2Request(ctx, http.MethodGet, url, nil, &raw); err != nil {
		return nil, err
	}

	var envelope deviceListResponse
	if err := json.Unmarshal(fixRadioTableBytes(raw), &envelope); err != nil {
		return nil, fmt.Errorf("decoding device envelope: %w", err)
	}

	if envelope.Meta.RC != "" && envelope.Meta.RC != "ok" {
		return nil, fmt.Errorf("controller returned rc=%s msg=%s", envelope.Meta.RC, envelope.Meta.Msg)
	}

	devices := make([]unifi.Device, 0, len(envelope.Data))
	for i, item := range envelope.Data {
		var d unifi.Device
		if err := json.Unmarshal(item, &d); err != nil {
			return nil, fmt.Errorf("decoding device[%d]: %w", i, err)
		}
		devices = append(devices, d)
	}
	return devices, nil
}

// ListDevice returns all devices for a site, working around the SDK's
// tx_power and channel unmarshal bugs.
func (c *Client) ListDevice(ctx context.Context, site string) ([]unifi.Device, error) {
	return c.fetchDeviceList(ctx, site, "")
}

// GetDeviceByMAC fetches a device by MAC, working around the SDK's tx_power
// and channel unmarshal bugs. The controller's stat/device/<mac> endpoint
// returns a single device when a MAC is supplied.
func (c *Client) GetDeviceByMAC(ctx context.Context, site, mac string) (*unifi.Device, error) {
	devices, err := c.fetchDeviceList(ctx, site, strings.ToLower(mac))
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, &unifi.NotFoundError{}
	}
	d := devices[0]
	return &d, nil
}

// fetchDevicePortOverridesRaw fetches one device's port_overrides array as raw
// JSON blobs (not decoded into unifi.DevicePortOverrides), so unmanaged ports'
// exact wire bytes can be passed straight through the merge/PUT untouched —
// avoiding the SDK's DevicePortOverrides struct, whose omitempty tags on every
// boolean field would silently drop e.g. "autoneg": false on a normal
// marshal/unmarshal round trip. See UpdateDevice for how this is used.
func (c *Client) fetchDevicePortOverridesRaw(ctx context.Context, site, mac string) ([]json.RawMessage, error) {
	url := fmt.Sprintf("%s%s/api/s/%s/stat/device/%s", c.BaseURL, c.APIPath, site, strings.ToLower(mac))
	var envelope struct {
		Meta struct {
			RC  string `json:"rc"`
			Msg string `json:"msg,omitempty"`
		} `json:"meta"`
		Data []struct {
			PortOverrides []json.RawMessage `json:"port_overrides"`
		} `json:"data"`
	}
	if err := c.doV2Request(ctx, http.MethodGet, url, nil, &envelope); err != nil {
		return nil, err
	}
	if envelope.Meta.RC != "" && envelope.Meta.RC != "ok" {
		return nil, fmt.Errorf("controller returned rc=%s msg=%s", envelope.Meta.RC, envelope.Meta.Msg)
	}
	if len(envelope.Data) == 0 {
		return nil, &unifi.NotFoundError{}
	}
	return envelope.Data[0].PortOverrides, nil
}

// GetDevice fetches a device by its controller ID. The v1 stat/device endpoint
// only supports lookup by MAC, so we list and filter, matching the SDK's
// behavior while applying our tx_power and channel workarounds.
func (c *Client) GetDevice(ctx context.Context, site, id string) (*unifi.Device, error) {
	devices, err := c.ListDevice(ctx, site)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		if devices[i].ID == id {
			return &devices[i], nil
		}
	}
	return nil, &unifi.NotFoundError{}
}
