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
		idx := int64(15)
		speed := int64(2500)
		existing := &unifi.DevicePortOverrides{
			PortIDX:         &idx,
			Forward:         "all",
			NATiveNetworkID: "net-default",
			PoeMode:         "auto",
			Speed:           &speed,
			FullDuplex:      true,
			Isolation:       true,
			Name:            "Uplink",
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

		applyPlannedToPortOverrideEntry(context.Background(), existing, planned)

		// Modeled + set fields updated.
		assert.Equal(t, "customize", existing.Forward)
		assert.Equal(t, "net-infra", existing.NATiveNetworkID)
		// Modeled but null-in-plan field left untouched.
		assert.Equal(t, "auto", existing.PoeMode)
		// Unmodeled fields (speed/duplex/isolation/name) survive unchanged —
		// this is the core safety property the merge logic must guarantee.
		require.NotNil(t, existing.Speed)
		assert.Equal(t, int64(2500), *existing.Speed)
		assert.True(t, existing.FullDuplex)
		assert.True(t, existing.Isolation)
		assert.Equal(t, "Uplink", existing.Name)
	})

	t.Run("sets excluded_network_ids when configured", func(t *testing.T) {
		idx := int64(18)
		existing := &unifi.DevicePortOverrides{PortIDX: &idx}
		planned := devicePortOverrideModel{
			PortIDX: types.Int64Value(18),
			ExcludedNetworkIDs: types.ListValueMust(types.StringType, []attr.Value{
				types.StringValue("net-default"),
				types.StringValue("net-work"),
			}),
		}
		applyPlannedToPortOverrideEntry(context.Background(), existing, planned)
		assert.Equal(t, []string{"net-default", "net-work"}, existing.ExcludedNetworkIDs)
	})

	t.Run("leaves excluded_network_ids untouched when null in plan", func(t *testing.T) {
		idx := int64(18)
		existing := &unifi.DevicePortOverrides{
			PortIDX:            &idx,
			ExcludedNetworkIDs: []string{"net-default"},
		}
		planned := devicePortOverrideModel{
			PortIDX:            types.Int64Value(18),
			ExcludedNetworkIDs: types.ListNull(types.StringType),
		}
		applyPlannedToPortOverrideEntry(context.Background(), existing, planned)
		assert.Equal(t, []string{"net-default"}, existing.ExcludedNetworkIDs)
	})
}

func TestUpdateDevicePayload_portOverrides_touchesOnlyDeclaredPorts(t *testing.T) {
	// Verifies the port-level half of the merge directly against the
	// plannedByPort map-building logic used in UpdateDevice: a plan
	// declaring port 15 must never produce a payload entry that mutates
	// port 17's data. This exercises the same map-keying logic UpdateDevice
	// uses, without requiring an HTTP round trip.
	idx15 := int64(15)
	idx17 := int64(17)
	m := &deviceResourceModel{
		PortOverrides: []devicePortOverrideModel{
			{PortIDX: types.Int64Value(15), Forward: types.StringValue("customize")},
		},
	}
	existingPorts := []unifi.DevicePortOverrides{
		{PortIDX: &idx15, Forward: "all", Name: "Port 15"},
		{PortIDX: &idx17, Forward: "all", Name: "Port 17"},
	}

	plannedByPort := map[int64]*devicePortOverrideModel{}
	for i := range m.PortOverrides {
		if m.PortOverrides[i].PortIDX.IsNull() || m.PortOverrides[i].PortIDX.IsUnknown() {
			continue
		}
		plannedByPort[m.PortOverrides[i].PortIDX.ValueInt64()] = &m.PortOverrides[i]
	}

	merged := make([]unifi.DevicePortOverrides, len(existingPorts))
	copy(merged, existingPorts)
	for i := range merged {
		if merged[i].PortIDX == nil {
			continue
		}
		if planned, ok := plannedByPort[*merged[i].PortIDX]; ok {
			applyPlannedToPortOverrideEntry(context.Background(), &merged[i], *planned)
		}
	}

	require.Len(t, merged, 2)
	assert.Equal(t, "customize", merged[0].Forward, "declared port 15 should be updated")
	assert.Equal(t, "all", merged[1].Forward, "undeclared port 17 must be untouched")
	assert.Equal(t, "Port 17", merged[1].Name)
}

func TestUpdateDevicePayload_portOverrides_errorsOnUnmatchedPortIdx(t *testing.T) {
	// Verifies the matched-tracking half of the port-level merge used in
	// UpdateDevice: a plan declaring a port_idx the device doesn't actually
	// have (e.g. port 20 on a 16-port switch) must not silently no-op —
	// it must surface as an error rather than a successful apply that
	// changed nothing. This replicates the matched-tracking logic in
	// UpdateDevice's port-merge block without requiring an HTTP round trip.
	idx15 := int64(15)
	m := &deviceResourceModel{
		PortOverrides: []devicePortOverrideModel{
			{PortIDX: types.Int64Value(15), Forward: types.StringValue("customize")},
			{PortIDX: types.Int64Value(20), Forward: types.StringValue("customize")},
		},
	}
	existingPorts := []unifi.DevicePortOverrides{
		{PortIDX: &idx15, Forward: "all", Name: "Port 15"},
	}

	plannedByPort := map[int64]*devicePortOverrideModel{}
	for i := range m.PortOverrides {
		if m.PortOverrides[i].PortIDX.IsNull() || m.PortOverrides[i].PortIDX.IsUnknown() {
			continue
		}
		plannedByPort[m.PortOverrides[i].PortIDX.ValueInt64()] = &m.PortOverrides[i]
	}

	merged := make([]unifi.DevicePortOverrides, len(existingPorts))
	copy(merged, existingPorts)
	matched := make(map[int64]bool, len(plannedByPort))
	for i := range merged {
		if merged[i].PortIDX == nil {
			continue
		}
		if planned, ok := plannedByPort[*merged[i].PortIDX]; ok {
			applyPlannedToPortOverrideEntry(context.Background(), &merged[i], *planned)
			matched[*merged[i].PortIDX] = true
		}
	}

	var missing []int64
	for idx := range plannedByPort {
		if !matched[idx] {
			missing = append(missing, idx)
		}
	}

	require.Len(t, missing, 1, "port_idx 20 has no matching live entry and must be reported as missing")
	assert.Equal(t, int64(20), missing[0])
	// Port 15, which did match, should still have been applied — the error
	// path doesn't retroactively undo work already done in the loop (the
	// real UpdateDevice discards the payload entirely on error, since it
	// returns before setting payload.PortOverrides).
	assert.Equal(t, "customize", merged[0].Forward)
}
