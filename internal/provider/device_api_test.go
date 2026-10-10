package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ubiquiti-community/go-unifi/unifi"
)

// Verifies the workaround that wraps numeric tx_power and channel values in
// quotes so the SDK's DeviceRadioTable.UnmarshalJSON does not fail on payloads
// from controllers that emit either field as a JSON number.
func TestFixRadioTableBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "numeric tx_power gets quoted",
			in:   `{"tx_power":23}`,
			want: `{"tx_power":"23"}`,
		},
		{
			name: "negative numeric tx_power gets quoted",
			in:   `{"tx_power":-5}`,
			want: `{"tx_power":"-5"}`,
		},
		{
			name: "fractional tx_power gets quoted",
			in:   `{"tx_power": 1.5}`,
			want: `{"tx_power":"1.5"}`,
		},
		{
			name: "string tx_power is left alone",
			in:   `{"tx_power":"23"}`,
			want: `{"tx_power":"23"}`,
		},
		{
			name: "auto string is left alone",
			in:   `{"tx_power":"auto"}`,
			want: `{"tx_power":"auto"}`,
		},
		{
			name: "tx_power_mode is not affected",
			in:   `{"tx_power_mode":"auto","tx_power":18}`,
			want: `{"tx_power_mode":"auto","tx_power":"18"}`,
		},
		{
			name: "multiple radio entries all coerced",
			in:   `[{"tx_power":18},{"tx_power":23}]`,
			want: `[{"tx_power":"18"},{"tx_power":"23"}]`,
		},
		{
			name: "numeric channel gets quoted",
			in:   `{"channel":36}`,
			want: `{"channel":"36"}`,
		},
		{
			name: "string channel is left alone",
			in:   `{"channel":"auto"}`,
			want: `{"channel":"auto"}`,
		},
		{
			name: "numeric channel and tx_power both coerced",
			in:   `{"channel":149,"tx_power":18}`,
			want: `{"channel":"149","tx_power":"18"}`,
		},
		{
			name: "fractional channel gets quoted",
			in:   `{"channel": 2.5}`,
			want: `{"channel":"2.5"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(fixRadioTableBytes([]byte(tc.in)))
			assert.Equal(t, tc.want, got)
		})
	}
}

// Verifies that a stat/device payload with numeric tx_power and channel
// values decodes cleanly after the workaround is applied. Without the
// workaround, the SDK's DeviceRadioTable.UnmarshalJSON returns "cannot
// unmarshal number into Go struct field .Alias.tx_power of type string" or
// the equivalent for channel.
func TestFixRadioTableBytes_decodesIntoUnifiDevice(t *testing.T) {
	const payload = `{
		"meta": {"rc": "ok"},
		"data": [{
			"_id": "abc123",
			"mac": "aa:bb:cc:dd:ee:ff",
			"name": "AP",
			"radio_table": [
				{"radio": "ng", "channel": 6, "ht": 40, "tx_power": 18, "tx_power_mode": "auto"},
				{"radio": "na", "channel": 149, "ht": 80, "tx_power": "23", "tx_power_mode": "high"}
			]
		}]
	}`

	// Sanity: raw payload fails to decode without the workaround.
	var rawEnvelope struct {
		Data []unifi.Device `json:"data"`
	}
	rawErr := json.Unmarshal([]byte(payload), &rawEnvelope)
	require.Error(t, rawErr, "expected SDK to reject numeric tx_power/channel without workaround")

	patched := fixRadioTableBytes([]byte(payload))
	var envelope struct {
		Data []unifi.Device `json:"data"`
	}
	require.NoError(t, json.Unmarshal(patched, &envelope))
	require.Len(t, envelope.Data, 1)

	d := envelope.Data[0]
	require.Len(t, d.RadioTable, 2)
	assert.Equal(t, "6", d.RadioTable[0].Channel)
	assert.Equal(t, "18", d.RadioTable[0].TxPower)
	assert.Equal(t, "auto", d.RadioTable[0].TxPowerMode)
	assert.Equal(t, "149", d.RadioTable[1].Channel)
	assert.Equal(t, "23", d.RadioTable[1].TxPower)
	assert.Equal(t, "high", d.RadioTable[1].TxPowerMode)
}

func TestApplyPlannedToPortOverrideEntry(t *testing.T) {
	t.Run("updates only non-null modeled fields, preserves everything else", func(t *testing.T) {
		entry := map[string]any{
			"port_idx":              float64(15),
			"forward":               "all",
			"native_networkconf_id": "net-default",
			"poe_mode":              "auto",
			"speed":                 float64(2500),
			"full_duplex":           true,
			"isolation":             true,
			"name":                  "Uplink",
		}
		planned := devicePortOverrideModel{
			PortIDX:            types.Int64Value(15),
			Forward:            types.StringValue("customize"),
			NativeNetworkID:    types.StringValue("net-infra"),
			TaggedVLANMgmt:     types.StringNull(),
			PoeMode:            types.StringNull(),
			OpMode:             types.StringNull(),
			ExcludedNetworkIDs: types.ListNull(types.StringType),
		}

		applyPlannedToPortOverrideEntry(context.Background(), entry, planned)

		// Modeled + set fields updated.
		assert.Equal(t, "customize", entry["forward"])
		assert.Equal(t, "net-infra", entry["native_networkconf_id"])
		// Modeled but null-in-plan field left untouched.
		assert.Equal(t, "auto", entry["poe_mode"])
		// Unmodeled fields (speed/duplex/isolation/name) survive unchanged —
		// this is the core safety property the merge logic must guarantee.
		assert.Equal(t, float64(2500), entry["speed"])
		assert.Equal(t, true, entry["full_duplex"])
		assert.Equal(t, true, entry["isolation"])
		assert.Equal(t, "Uplink", entry["name"])
	})

	t.Run("sets excluded_network_ids when configured", func(t *testing.T) {
		entry := map[string]any{"port_idx": float64(18)}
		planned := devicePortOverrideModel{
			PortIDX: types.Int64Value(18),
			ExcludedNetworkIDs: types.ListValueMust(types.StringType, []attr.Value{
				types.StringValue("net-default"),
				types.StringValue("net-work"),
			}),
		}
		applyPlannedToPortOverrideEntry(context.Background(), entry, planned)
		assert.Equal(t, []string{"net-default", "net-work"}, entry["excluded_networkconf_ids"])
	})

	t.Run("leaves excluded_network_ids untouched when null in plan", func(t *testing.T) {
		entry := map[string]any{
			"port_idx":                 float64(18),
			"excluded_networkconf_ids": []string{"net-default"},
		}
		planned := devicePortOverrideModel{
			PortIDX:            types.Int64Value(18),
			ExcludedNetworkIDs: types.ListNull(types.StringType),
		}
		applyPlannedToPortOverrideEntry(context.Background(), entry, planned)
		assert.Equal(t, []string{"net-default"}, entry["excluded_networkconf_ids"])
	})

	// This is the test that would have caught Finding 1: the old
	// implementation operated on the typed unifi.DevicePortOverrides struct,
	// whose `omitempty` tag on every boolean field silently drops "false"
	// values on marshal. Operating on map[string]any and asserting on the
	// actual marshaled JSON bytes (not just the in-memory Go value) proves
	// the field survives serialization onto the wire.
	t.Run("unmodeled false boolean survives serialization", func(t *testing.T) {
		entry := map[string]any{
			"port_idx":               float64(15),
			"autoneg":                false,
			"stp_port_mode":          false,
			"flow_control_enabled":   false,
			"lldpmed_enabled":        false,
			"port_keepalive_enabled": false,
		}
		planned := devicePortOverrideModel{
			PortIDX: types.Int64Value(15),
			PoeMode: types.StringValue("off"), // touch an unrelated field
		}

		applyPlannedToPortOverrideEntry(context.Background(), entry, planned)

		b, err := json.Marshal(entry)
		require.NoError(t, err)
		s := string(b)

		assert.Contains(t, s, `"autoneg":false`)
		assert.Contains(t, s, `"stp_port_mode":false`)
		assert.Contains(t, s, `"flow_control_enabled":false`)
		assert.Contains(t, s, `"lldpmed_enabled":false`)
		assert.Contains(t, s, `"port_keepalive_enabled":false`)
		assert.Contains(t, s, `"poe_mode":"off"`)
	})
}

// mergePortOverridesForTest replicates the raw-JSON merge logic in
// UpdateDevice's port-merge block (see device_api.go), so the port-level
// merge semantics (declared-port isolation and append-on-no-match) can be
// exercised directly against []json.RawMessage without an HTTP round trip.
func mergePortOverridesForTest(t *testing.T, rawPorts []json.RawMessage, plannedByPort map[int64]*devicePortOverrideModel) []map[string]any {
	t.Helper()

	merged := make([]json.RawMessage, 0, len(rawPorts)+len(plannedByPort))
	matched := make(map[int64]bool, len(plannedByPort))

	for _, raw := range rawPorts {
		var entry map[string]any
		require.NoError(t, json.Unmarshal(raw, &entry))
		idxFloat, ok := entry["port_idx"].(float64)
		if !ok {
			merged = append(merged, raw)
			continue
		}
		idx := int64(idxFloat)
		planned, ok := plannedByPort[idx]
		if !ok {
			merged = append(merged, raw)
			continue
		}
		applyPlannedToPortOverrideEntry(context.Background(), entry, *planned)
		b, err := json.Marshal(entry)
		require.NoError(t, err)
		merged = append(merged, b)
		matched[idx] = true
	}

	for idx, planned := range plannedByPort {
		if matched[idx] {
			continue
		}
		entry := map[string]any{"port_idx": idx}
		applyPlannedToPortOverrideEntry(context.Background(), entry, *planned)
		b, err := json.Marshal(entry)
		require.NoError(t, err)
		merged = append(merged, b)
	}

	result := make([]map[string]any, len(merged))
	for i, raw := range merged {
		var entry map[string]any
		require.NoError(t, json.Unmarshal(raw, &entry))
		result[i] = entry
	}
	return result
}

func buildPlannedByPort(m *deviceResourceModel) map[int64]*devicePortOverrideModel {
	plannedByPort := map[int64]*devicePortOverrideModel{}
	for i := range m.PortOverrides {
		if m.PortOverrides[i].PortIDX.IsNull() || m.PortOverrides[i].PortIDX.IsUnknown() {
			continue
		}
		plannedByPort[m.PortOverrides[i].PortIDX.ValueInt64()] = &m.PortOverrides[i]
	}
	return plannedByPort
}

func TestUpdateDevicePayload_portOverrides_touchesOnlyDeclaredPorts(t *testing.T) {
	// Verifies the port-level half of the merge: a plan declaring port 15
	// must never produce a payload entry that mutates port 17's data.
	m := &deviceResourceModel{
		PortOverrides: []devicePortOverrideModel{
			{PortIDX: types.Int64Value(15), Forward: types.StringValue("customize")},
		},
	}
	rawPorts := []json.RawMessage{
		json.RawMessage(`{"port_idx":15,"forward":"all","name":"Port 15"}`),
		json.RawMessage(`{"port_idx":17,"forward":"all","name":"Port 17"}`),
	}

	merged := mergePortOverridesForTest(t, rawPorts, buildPlannedByPort(m))

	require.Len(t, merged, 2)
	assert.Equal(t, "customize", merged[0]["forward"], "declared port 15 should be updated")
	assert.Equal(t, "all", merged[1]["forward"], "undeclared port 17 must be untouched")
	assert.Equal(t, "Port 17", merged[1]["name"])
}

func TestUpdateDevicePayload_portOverrides_appendsUnmatchedPortIdx(t *testing.T) {
	// Verifies Finding 2's fix: a declared port_idx with no existing live
	// entry (e.g. a factory-default port that has never been customized)
	// must get a brand-new entry appended, not error out. Existing entries
	// must be left untouched.
	m := &deviceResourceModel{
		PortOverrides: []devicePortOverrideModel{
			{PortIDX: types.Int64Value(15), Forward: types.StringValue("customize")},
			{PortIDX: types.Int64Value(20), PoeMode: types.StringValue("off")},
		},
	}
	rawPorts := []json.RawMessage{
		json.RawMessage(`{"port_idx":15,"forward":"all","name":"Port 15"}`),
	}

	merged := mergePortOverridesForTest(t, rawPorts, buildPlannedByPort(m))

	require.Len(t, merged, 2)
	assert.Equal(t, "customize", merged[0]["forward"], "existing port 15 entry should be updated")
	assert.Equal(t, "Port 15", merged[0]["name"], "existing port 15's unmodeled fields survive")

	assert.Equal(t, float64(20), merged[1]["port_idx"], "new entry appended for port 20")
	assert.Equal(t, "off", merged[1]["poe_mode"])
	assert.NotContains(t, merged[1], "forward", "new entry only contains modeled+set fields")
}

func TestDeviceUpdatePayload_mgmtNetworkID(t *testing.T) {
	// Verifies the Network Override is sent when set and omitted when not, so
	// a config that doesn't declare it leaves the controller's value alone.
	set, err := json.Marshal(deviceUpdatePayload{Name: "sw", MgmtNetworkID: "net-96"})
	require.NoError(t, err)
	assert.Contains(t, string(set), `"mgmt_network_id":"net-96"`)

	unset, err := json.Marshal(deviceUpdatePayload{Name: "sw"})
	require.NoError(t, err)
	assert.NotContains(t, string(unset), "mgmt_network_id")
}

func TestDeviceMgmtNetworkID_readAndPreserveNull(t *testing.T) {
	r := &deviceResource{}

	var read deviceResourceModel
	r.apiToModel(&unifi.Device{MAC: "aa:bb:cc:dd:ee:ff", MgmtNetworkID: "net-96"}, &read, "default")
	assert.Equal(t, types.StringValue("net-96"), read.MgmtNetworkID)

	var unsetOnController deviceResourceModel
	r.apiToModel(&unifi.Device{MAC: "aa:bb:cc:dd:ee:ff"}, &unsetOnController, "default")
	assert.True(t, unsetOnController.MgmtNetworkID.IsNull(), "empty override reads back as null")

	// A config that doesn't declare the attribute must not see the controller's
	// value as drift.
	plan := deviceResourceModel{MgmtNetworkID: types.StringNull()}
	r.preserveNullOptionals(&plan, &read)
	assert.True(t, read.MgmtNetworkID.IsNull())

	declared := deviceResourceModel{MgmtNetworkID: types.StringValue("net-96")}
	var live deviceResourceModel
	r.apiToModel(&unifi.Device{MAC: "aa:bb:cc:dd:ee:ff", MgmtNetworkID: "net-96"}, &live, "default")
	r.preserveNullOptionals(&declared, &live)
	assert.Equal(t, types.StringValue("net-96"), live.MgmtNetworkID)
}
