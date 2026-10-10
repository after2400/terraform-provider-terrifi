package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/ubiquiti-community/go-unifi/unifi"
)

var (
	_ resource.Resource                = &portForwardResource{}
	_ resource.ResourceWithImportState = &portForwardResource{}

	portListPattern = regexp.MustCompile(`^\d{1,5}(-\d{1,5})?(,\d{1,5}(-\d{1,5})?)*$`)
	wanIfacePattern = regexp.MustCompile(`^(wan[2-9]?|both|all)$`)
	srcLimitIP      = "ip"
	srcLimitFWGroup = "firewall_group"
)

func NewPortForwardResource() resource.Resource {
	return &portForwardResource{}
}

type portForwardResource struct {
	client *Client
}

type portForwardResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	Site                  types.String `tfsdk:"site"`
	Name                  types.String `tfsdk:"name"`
	Enabled               types.Bool   `tfsdk:"enabled"`
	Protocol              types.String `tfsdk:"protocol"`
	Interface             types.String `tfsdk:"interface"`
	DestinationIP         types.String `tfsdk:"destination_ip"`
	DestinationPort       types.String `tfsdk:"destination_port"`
	ForwardIP             types.String `tfsdk:"forward_ip"`
	ForwardPort           types.String `tfsdk:"forward_port"`
	SourceIP              types.String `tfsdk:"source_ip"`
	SourceFirewallGroupID types.String `tfsdk:"source_firewall_group_id"`
	Log                   types.Bool   `tfsdk:"log"`
}

func (r *portForwardResource) Metadata(
	_ context.Context,
	req resource.MetadataRequest,
	resp *resource.MetadataResponse,
) {
	resp.TypeName = req.ProviderTypeName + "_port_forward"
}

func (r *portForwardResource) Schema(
	_ context.Context,
	_ resource.SchemaRequest,
	resp *resource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a port forward (NAT rule) on the UniFi controller. " +
			"Whether a forward also needs a firewall policy under Zone-Based Firewall depends on the controller version; check your rules after creating one.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The ID of the port forward.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"site": schema.StringAttribute{
				MarkdownDescription: "The site to associate the port forward with. Defaults to the provider site.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},

			"name": schema.StringAttribute{
				MarkdownDescription: "A name for the rule (1-128 characters).",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(1, 128),
				},
			},

			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the rule is enabled. Default: `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},

			"protocol": schema.StringAttribute{
				MarkdownDescription: "Protocol: `tcp_udp`, `tcp` or `udp`. Default: `tcp_udp`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("tcp_udp"),
				Validators: []validator.String{
					stringvalidator.OneOf("tcp_udp", "tcp", "udp"),
				},
			},

			"interface": schema.StringAttribute{
				MarkdownDescription: "WAN interface the rule listens on: `wan`, `wan2` to `wan9`, `both` or `all`. Default: `wan`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("wan"),
				Validators: []validator.String{
					stringvalidator.RegexMatches(wanIfacePattern, "must be wan, wan2-wan9, both or all"),
				},
			},

			"destination_ip": schema.StringAttribute{
				MarkdownDescription: "WAN-side destination IP the rule matches, or `any`. Default: `any`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("any"),
			},

			"destination_port": schema.StringAttribute{
				MarkdownDescription: "External port, a range (`3074-3080`) or a comma-separated list of either (`80,443,8000-8010`).",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(portListPattern, "must be a port, a range, or a comma-separated list of them"),
				},
			},

			"forward_ip": schema.StringAttribute{
				MarkdownDescription: "LAN IPv4 address the traffic is forwarded to.",
				Required:            true,
			},

			"forward_port": schema.StringAttribute{
				MarkdownDescription: "Port, range or comma-separated list on the forward target.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(portListPattern, "must be a port, a range, or a comma-separated list of them"),
				},
			},

			"source_ip": schema.StringAttribute{
				MarkdownDescription: "Restrict the rule to this source: an IP, an `a.b.c.d-e.f.g.h` range or a CIDR, optionally prefixed with `!` to negate. " +
					"Omit to allow any source. Cannot be combined with `source_firewall_group_id`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("source_firewall_group_id")),
				},
			},

			"source_firewall_group_id": schema.StringAttribute{
				MarkdownDescription: "Restrict the rule to sources in this firewall group (`terrifi_firewall_group.<name>.id`). " +
					"Cannot be combined with `source_ip`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRoot("source_ip")),
				},
			},

			"log": schema.BoolAttribute{
				MarkdownDescription: "Whether to log matching traffic. Default: `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
		},
	}
}

func (r *portForwardResource) Configure(
	_ context.Context,
	req resource.ConfigureRequest,
	resp *resource.ConfigureResponse,
) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *Client, got: %T.", req.ProviderData),
		)
		return
	}
	r.client = client
}

func (r *portForwardResource) Create(
	ctx context.Context,
	req resource.CreateRequest,
	resp *resource.CreateResponse,
) {
	var plan portForwardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site := r.client.SiteOrDefault(plan.Site)
	created, err := r.client.CreatePortForward(ctx, site, r.modelToAPI(&plan))
	if err != nil {
		resp.Diagnostics.AddError("Error Creating Port Forward", err.Error())
		return
	}

	r.apiToModel(created, &plan, site)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *portForwardResource) Read(
	ctx context.Context,
	req resource.ReadRequest,
	resp *resource.ReadResponse,
) {
	var state portForwardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site := r.client.SiteOrDefault(state.Site)
	pf, err := r.client.GetPortForward(ctx, site, state.ID.ValueString())
	if err != nil {
		if _, ok := err.(*unifi.NotFoundError); ok {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Error Reading Port Forward",
			fmt.Sprintf("Could not read port forward %s: %s", state.ID.ValueString(), err.Error()),
		)
		return
	}

	r.apiToModel(pf, &state, site)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *portForwardResource) Update(
	ctx context.Context,
	req resource.UpdateRequest,
	resp *resource.UpdateResponse,
) {
	var state, plan portForwardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site := r.client.SiteOrDefault(state.Site)

	// The PUT replaces the whole object, so keep fields we don't model
	// (per-WAN destination IPs) from the live rule.
	existing, err := r.client.GetPortForward(ctx, site, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error Reading Port Forward for Update", err.Error())
		return
	}

	pf := r.modelToAPI(&plan)
	pf.ID = state.ID.ValueString()
	pf.DestinationIPs = existing.DestinationIPs

	updated, err := r.client.UpdatePortForward(ctx, site, pf)
	if err != nil {
		resp.Diagnostics.AddError("Error Updating Port Forward", err.Error())
		return
	}

	r.apiToModel(updated, &plan, site)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *portForwardResource) Delete(
	ctx context.Context,
	req resource.DeleteRequest,
	resp *resource.DeleteResponse,
) {
	var state portForwardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	site := r.client.SiteOrDefault(state.Site)
	if err := r.client.DeletePortForward(ctx, site, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Error Deleting Port Forward", err.Error())
	}
}

// ImportState accepts "site:id" or just "id".
func (r *portForwardResource) ImportState(
	ctx context.Context,
	req resource.ImportStateRequest,
	resp *resource.ImportStateResponse,
) {
	parts := strings.SplitN(req.ID, ":", 2)
	if len(parts) == 2 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("site"), parts[0])...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *portForwardResource) modelToAPI(m *portForwardResourceModel) *unifi.PortForward {
	pf := &unifi.PortForward{
		Name:          m.Name.ValueString(),
		Enabled:       m.Enabled.ValueBool(),
		Proto:         m.Protocol.ValueString(),
		PfwdInterface: m.Interface.ValueString(),
		DestinationIP: m.DestinationIP.ValueString(),
		DstPort:       m.DestinationPort.ValueString(),
		Fwd:           m.ForwardIP.ValueString(),
		FwdPort:       m.ForwardPort.ValueString(),
		Log:           m.Log.ValueBool(),
	}

	switch {
	case !m.SourceIP.IsNull() && !m.SourceIP.IsUnknown():
		pf.SrcLimitingEnabled = true
		pf.SrcLimitingType = srcLimitIP
		pf.Src = m.SourceIP.ValueString()
	case !m.SourceFirewallGroupID.IsNull() && !m.SourceFirewallGroupID.IsUnknown():
		pf.SrcLimitingEnabled = true
		pf.SrcLimitingType = srcLimitFWGroup
		pf.SrcFirewallGroupID = m.SourceFirewallGroupID.ValueString()
	}

	return pf
}

func (r *portForwardResource) apiToModel(pf *unifi.PortForward, m *portForwardResourceModel, site string) {
	m.ID = types.StringValue(pf.ID)
	m.Site = types.StringValue(site)
	m.Name = types.StringValue(pf.Name)
	m.Enabled = types.BoolValue(pf.Enabled)
	m.Protocol = types.StringValue(pf.Proto)
	m.Interface = types.StringValue(pf.PfwdInterface)
	m.DestinationIP = types.StringValue(pf.DestinationIP)
	m.DestinationPort = types.StringValue(pf.DstPort)
	m.ForwardIP = types.StringValue(pf.Fwd)
	m.ForwardPort = types.StringValue(pf.FwdPort)
	m.Log = types.BoolValue(pf.Log)

	m.SourceIP = types.StringNull()
	m.SourceFirewallGroupID = types.StringNull()
	if pf.SrcLimitingEnabled {
		switch pf.SrcLimitingType {
		case srcLimitIP:
			if pf.Src != "" && pf.Src != "any" {
				m.SourceIP = types.StringValue(pf.Src)
			}
		case srcLimitFWGroup:
			if pf.SrcFirewallGroupID != "" {
				m.SourceFirewallGroupID = types.StringValue(pf.SrcFirewallGroupID)
			}
		}
	}
}
