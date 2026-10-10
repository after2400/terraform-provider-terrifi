package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ubiquiti-community/go-unifi/unifi"
)

func TestPortForwardModelToAPI(t *testing.T) {
	r := &portForwardResource{}
	base := portForwardResourceModel{
		Name:            types.StringValue("game"),
		Enabled:         types.BoolValue(true),
		Protocol:        types.StringValue("tcp_udp"),
		Interface:       types.StringValue("wan"),
		DestinationIP:   types.StringValue("any"),
		DestinationPort: types.StringValue("3074"),
		ForwardIP:       types.StringValue("10.0.0.5"),
		ForwardPort:     types.StringValue("3074"),
		Log:             types.BoolValue(false),
	}

	t.Run("no source restriction", func(t *testing.T) {
		m := base
		m.SourceIP = types.StringNull()
		m.SourceFirewallGroupID = types.StringNull()
		pf := r.modelToAPI(&m)
		assert.Equal(t, "game", pf.Name)
		assert.Equal(t, "tcp_udp", pf.Proto)
		assert.Equal(t, "3074", pf.DstPort)
		assert.Equal(t, "10.0.0.5", pf.Fwd)
		assert.True(t, pf.Enabled)
		assert.False(t, pf.SrcLimitingEnabled)
		assert.Empty(t, pf.SrcLimitingType)
	})

	t.Run("source ip", func(t *testing.T) {
		m := base
		m.SourceIP = types.StringValue("203.0.113.0/24")
		m.SourceFirewallGroupID = types.StringNull()
		pf := r.modelToAPI(&m)
		assert.True(t, pf.SrcLimitingEnabled)
		assert.Equal(t, "ip", pf.SrcLimitingType)
		assert.Equal(t, "203.0.113.0/24", pf.Src)
	})

	t.Run("source firewall group", func(t *testing.T) {
		m := base
		m.SourceIP = types.StringNull()
		m.SourceFirewallGroupID = types.StringValue("grp1")
		pf := r.modelToAPI(&m)
		assert.True(t, pf.SrcLimitingEnabled)
		assert.Equal(t, "firewall_group", pf.SrcLimitingType)
		assert.Equal(t, "grp1", pf.SrcFirewallGroupID)
	})
}

func TestPortForwardAPIToModel(t *testing.T) {
	r := &portForwardResource{}

	t.Run("unrestricted rule as the controller returns it", func(t *testing.T) {
		var m portForwardResourceModel
		r.apiToModel(&unifi.PortForward{
			ID: "abc", Name: "game", Enabled: true, Proto: "udp", PfwdInterface: "wan",
			DestinationIP: "any", DstPort: "9002", Fwd: "10.0.0.5", FwdPort: "9002",
		}, &m, "default")
		assert.Equal(t, "abc", m.ID.ValueString())
		assert.Equal(t, "udp", m.Protocol.ValueString())
		assert.Equal(t, "9002", m.DestinationPort.ValueString())
		assert.True(t, m.SourceIP.IsNull())
		assert.True(t, m.SourceFirewallGroupID.IsNull())
		assert.False(t, m.Log.ValueBool())
	})

	t.Run("source restrictions are read back", func(t *testing.T) {
		var m portForwardResourceModel
		r.apiToModel(&unifi.PortForward{
			ID: "a", SrcLimitingEnabled: true, SrcLimitingType: "ip", Src: "203.0.113.7",
		}, &m, "default")
		assert.Equal(t, "203.0.113.7", m.SourceIP.ValueString())
		assert.True(t, m.SourceFirewallGroupID.IsNull())

		r.apiToModel(&unifi.PortForward{
			ID: "a", SrcLimitingEnabled: true, SrcLimitingType: "firewall_group", SrcFirewallGroupID: "g1",
		}, &m, "default")
		assert.True(t, m.SourceIP.IsNull())
		assert.Equal(t, "g1", m.SourceFirewallGroupID.ValueString())
	})

	t.Run("a stale src value is ignored when limiting is off", func(t *testing.T) {
		var m portForwardResourceModel
		r.apiToModel(&unifi.PortForward{ID: "a", Src: "203.0.113.7", SrcLimitingEnabled: false}, &m, "default")
		assert.True(t, m.SourceIP.IsNull())
	})
}

func TestPortForwardClient(t *testing.T) {
	var gotMethod, gotPath, gotKey string
	var gotBody map[string]any
	reply := `{"meta":{"rc":"ok"},"data":[{"_id":"id1","name":"game","enabled":true}]}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotMethod, gotPath, gotKey = req.Method, req.URL.Path, req.Header.Get("X-API-Key")
		gotBody = nil
		_ = json.NewDecoder(req.Body).Decode(&gotBody)
		fmt.Fprint(w, reply)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIPath: "/proxy/network", APIKey: "k", HTTP: retryablehttp.NewClient()}
	c.HTTP.RetryMax = 0
	ctx := context.Background()

	t.Run("get finds by id", func(t *testing.T) {
		pf, err := c.GetPortForward(ctx, "default", "id1")
		require.NoError(t, err)
		assert.Equal(t, "id1", pf.ID)
		assert.Equal(t, http.MethodGet, gotMethod)
		assert.Equal(t, "/proxy/network/api/s/default/rest/portforward", gotPath)
		assert.Equal(t, "k", gotKey)
	})

	t.Run("get returns NotFoundError for an unknown id", func(t *testing.T) {
		_, err := c.GetPortForward(ctx, "default", "nope")
		var nf *unifi.NotFoundError
		require.ErrorAs(t, err, &nf)
	})

	t.Run("create posts the body", func(t *testing.T) {
		pf, err := c.CreatePortForward(ctx, "default", &unifi.PortForward{Name: "game", Enabled: true})
		require.NoError(t, err)
		assert.Equal(t, "id1", pf.ID)
		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "game", gotBody["name"])
	})

	t.Run("update puts to the id path", func(t *testing.T) {
		_, err := c.UpdatePortForward(ctx, "default", &unifi.PortForward{ID: "id1", Name: "game"})
		require.NoError(t, err)
		assert.Equal(t, http.MethodPut, gotMethod)
		assert.Equal(t, "/proxy/network/api/s/default/rest/portforward/id1", gotPath)
	})

	t.Run("delete uses the id path", func(t *testing.T) {
		reply = `{"meta":{"rc":"ok"},"data":[]}`
		require.NoError(t, c.DeletePortForward(ctx, "default", "id1"))
		assert.Equal(t, http.MethodDelete, gotMethod)
		assert.Equal(t, "/proxy/network/api/s/default/rest/portforward/id1", gotPath)
	})

	t.Run("a 200 with rc=error is an error", func(t *testing.T) {
		reply = `{"meta":{"rc":"error","msg":"api.err.Invalid"},"data":[]}`
		_, err := c.CreatePortForward(ctx, "default", &unifi.PortForward{Name: "x"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "api.err.Invalid")
	})
}

// ---------------------------------------------------------------------------
// Acceptance tests
// ---------------------------------------------------------------------------

func TestAccPortForward_basic(t *testing.T) {
	name := fmt.Sprintf("tfacc-pf-%s", randomSuffix())
	cfg := func(port string) string {
		return fmt.Sprintf(`
resource "terrifi_port_forward" "test" {
  name             = %q
  destination_port = %q
  forward_ip       = "192.168.1.250"
  forward_port     = %q
}
`, name, port, port)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { preCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("33074"),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("terrifi_port_forward.test", "protocol", "tcp_udp"),
					resource.TestCheckResourceAttr("terrifi_port_forward.test", "interface", "wan"),
					resource.TestCheckResourceAttr("terrifi_port_forward.test", "destination_port", "33074"),
				),
			},
			{
				Config: cfg("33075"),
				Check:  resource.TestCheckResourceAttr("terrifi_port_forward.test", "destination_port", "33075"),
			},
			{
				ResourceName:      "terrifi_port_forward.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}
