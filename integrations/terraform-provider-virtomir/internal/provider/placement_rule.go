package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

type placementRuleResource struct{ c *client.Client }

func newPlacementRule() resource.Resource { return &placementRuleResource{} }

type placementRuleModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Kind     types.String `tfsdk:"kind"`
	Strength types.String `tfsdk:"strength"`
	Enabled  types.Bool   `tfsdk:"enabled"`
	Domains  types.Set    `tfsdk:"domains"`
	Hosts    types.Set    `tfsdk:"hosts"`
	Exclude  types.Bool   `tfsdk:"exclude"`
}

func (r *placementRuleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_placement_rule"
}

func (r *placementRuleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A cluster placement rule: vm_affinity, vm_anti_affinity, or vm_host.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Placement rule ID (16 hex digits).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Descriptive name for the placement rule.",
			},
			"kind": schema.StringAttribute{
				Required:    true,
				Description: "vm_affinity, vm_anti_affinity, or vm_host.",
			},
			"strength": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("must"),
				Description: "Constraint strength: must (hard requirement) or should (soft preference). Default: must.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the rule is actively enforced. Default: true.",
			},
			"domains": schema.SetAttribute{
				Required:    true,
				ElementType: types.StringType,
				Description: "VM UUIDs governed by this rule.",
			},
			"hosts": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Target cluster host IDs (for vm_host rules).",
			},
			"exclude": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
				Description: "For vm_host rules: true to exclude (must/should NOT run on hosts). Default: false.",
			},
		},
	}
}

func (r *placementRuleResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func hostSetToPtr(ctx context.Context, s types.Set) *[]string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	out := setStrings(ctx, s)
	return &out
}

func hostsFromPtr(h *[]string) []string {
	if h == nil {
		return nil
	}
	return *h
}

func (r *placementRuleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m placementRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule := client.PlacementRule{
		Name:     m.Name.ValueString(),
		Kind:     client.PlacementKind(m.Kind.ValueString()),
		Strength: client.PlacementStrength(m.Strength.ValueString()),
		Enabled:  m.Enabled.ValueBool(),
		Domains:  setStrings(ctx, m.Domains),
		Hosts:    hostSetToPtr(ctx, m.Hosts),
		Exclude:  m.Exclude.ValueBool(),
	}
	created, err := r.c.CreatePlacementRule(ctx, rule)
	if err != nil {
		resp.Diagnostics.AddError("Placement rule not created", err.Error())
		return
	}
	m.ID = types.StringValue(created.ID)
	m.Name = types.StringValue(created.Name)
	m.Kind = types.StringValue(string(created.Kind))
	m.Strength = types.StringValue(string(created.Strength))
	m.Enabled = types.BoolValue(created.Enabled)
	m.Exclude = types.BoolValue(created.Exclude)
	m.Domains = stringSetFrom(ctx, created.Domains, false)
	m.Hosts = stringSetFrom(ctx, hostsFromPtr(created.Hosts), m.Hosts.IsNull())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *placementRuleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m placementRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rule, err := r.c.PlacementRule(ctx, m.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Placement rule not read", err.Error())
		return
	}
	m.Name = types.StringValue(rule.Name)
	m.Kind = types.StringValue(string(rule.Kind))
	m.Strength = types.StringValue(string(rule.Strength))
	m.Enabled = types.BoolValue(rule.Enabled)
	m.Exclude = types.BoolValue(rule.Exclude)
	m.Domains = stringSetFrom(ctx, rule.Domains, false)
	m.Hosts = stringSetFrom(ctx, hostsFromPtr(rule.Hosts), m.Hosts.IsNull())
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *placementRuleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state placementRuleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	rule := client.PlacementRule{
		ID:       id,
		Name:     plan.Name.ValueString(),
		Kind:     client.PlacementKind(plan.Kind.ValueString()),
		Strength: client.PlacementStrength(plan.Strength.ValueString()),
		Enabled:  plan.Enabled.ValueBool(),
		Domains:  setStrings(ctx, plan.Domains),
		Hosts:    hostSetToPtr(ctx, plan.Hosts),
		Exclude:  plan.Exclude.ValueBool(),
	}
	if err := r.c.UpdatePlacementRule(ctx, id, rule); err != nil {
		resp.Diagnostics.AddError("Placement rule not updated", err.Error())
		return
	}
	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *placementRuleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m placementRuleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeletePlacementRule(ctx, m.ID.ValueString()); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Placement rule not deleted", err.Error())
	}
}

func (r *placementRuleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

var (
	_ resource.Resource                = (*placementRuleResource)(nil)
	_ resource.ResourceWithConfigure   = (*placementRuleResource)(nil)
	_ resource.ResourceWithImportState = (*placementRuleResource)(nil)
)
