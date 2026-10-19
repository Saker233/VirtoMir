package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/onurbaydas/virtomir/integrations/terraform-provider-virtomir/internal/client"
)

type projectResource struct{ c *client.Client }

func newProject() resource.Resource { return &projectResource{} }

type projectMemberModel struct {
	Kind types.String `tfsdk:"kind"`
	Name types.String `tfsdk:"name"`
	Role types.String `tfsdk:"role"`
}

var memberAttrTypes = map[string]attr.Type{
	"kind": types.StringType,
	"name": types.StringType,
	"role": types.StringType,
}

var memberObjectType = types.ObjectType{AttrTypes: memberAttrTypes}

type projectLimitsModel struct {
	Machines  types.Int64 `tfsdk:"machines"`
	VCPUs     types.Int64 `tfsdk:"vcpus"`
	MemoryMiB types.Int64 `tfsdk:"memory_mib"`
	DiskGiB   types.Int64 `tfsdk:"disk_gib"`
}

type projectModel struct {
	ID           types.String        `tfsdk:"id"`
	Name         types.String        `tfsdk:"name"`
	Description  types.String        `tfsdk:"description"`
	StoragePools types.Set           `tfsdk:"storage_pools"`
	Networks     types.Set           `tfsdk:"networks"`
	Limits       *projectLimitsModel `tfsdk:"limits"`
	Members      types.Set           `tfsdk:"members"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A VirtoMir project (tenant): groups machines, storage pools and networks with members and limits.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "The project's slug identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name of the project.",
			},
			"description": schema.StringAttribute{
				Optional:    true,
				Description: "Optional description of the project.",
			},
			"storage_pools": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Allowed storage pools for machines in this project.",
			},
			"networks": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "Allowed virtual networks for machines in this project.",
			},
			"limits": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Resource limits for the project.",
				Attributes: map[string]schema.Attribute{
					"machines": schema.Int64Attribute{
						Optional:    true,
						Description: "Maximum number of machines (0 for unlimited).",
					},
					"vcpus": schema.Int64Attribute{
						Optional:    true,
						Description: "Maximum total vCPUs (0 for unlimited).",
					},
					"memory_mib": schema.Int64Attribute{
						Optional:    true,
						Description: "Maximum total memory in MiB (0 for unlimited).",
					},
					"disk_gib": schema.Int64Attribute{
						Optional:    true,
						Description: "Maximum total disk in GiB (0 for unlimited).",
					},
				},
			},
			"members": schema.SetNestedAttribute{
				Optional:    true,
				Description: "Members belonging to the project.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"kind": schema.StringAttribute{
							Required:    true,
							Description: "Member kind: user or group.",
						},
						"name": schema.StringAttribute{
							Required:    true,
							Description: "User or group name.",
						},
						"role": schema.StringAttribute{
							Required:    true,
							Description: "Member role: viewer, operator or admin.",
						},
					},
				},
			},
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, _ *resource.ConfigureResponse) {
	r.c = clientFrom(req.ProviderData)
}

func setStrings(ctx context.Context, s types.Set) []string {
	var out []string
	if s.IsNull() || s.IsUnknown() {
		return out
	}
	_ = s.ElementsAs(ctx, &out, false)
	return out
}

func stringSetFrom(ctx context.Context, items []string, wasNull bool) types.Set {
	if len(items) == 0 && wasNull {
		return types.SetNull(types.StringType)
	}
	if items == nil {
		items = []string{}
	}
	s, _ := types.SetValueFrom(ctx, types.StringType, items)
	return s
}

func membersFromSet(ctx context.Context, s types.Set, d *diag.Diagnostics) []client.ProjectMember {
	if s.IsNull() || s.IsUnknown() {
		return []client.ProjectMember{}
	}
	var models []projectMemberModel
	d.Append(s.ElementsAs(ctx, &models, false)...)
	out := make([]client.ProjectMember, len(models))
	for i, m := range models {
		out[i] = client.ProjectMember{
			Kind: strOr(m.Kind),
			Name: strOr(m.Name),
			Role: strOr(m.Role),
		}
	}
	return out
}

func limitsFromModel(m *projectLimitsModel) *client.ProjectLimits {
	if m == nil {
		return nil
	}
	lim := &client.ProjectLimits{}
	if !m.Machines.IsNull() && !m.Machines.IsUnknown() {
		lim.Machines = int(m.Machines.ValueInt64())
	}
	if !m.VCPUs.IsNull() && !m.VCPUs.IsUnknown() {
		lim.VCPUs = int(m.VCPUs.ValueInt64())
	}
	if !m.MemoryMiB.IsNull() && !m.MemoryMiB.IsUnknown() {
		lim.MemoryMiB = m.MemoryMiB.ValueInt64()
	}
	if !m.DiskGiB.IsNull() && !m.DiskGiB.IsUnknown() {
		lim.DiskGiB = m.DiskGiB.ValueInt64()
	}
	return lim
}

func validateMembers(ctx context.Context, s types.Set, d *diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return
	}
	var members []projectMemberModel
	d.Append(s.ElementsAs(ctx, &members, false)...)
	for _, mb := range members {
		kind := strOr(mb.Kind)
		name := strOr(mb.Name)
		role := strOr(mb.Role)
		if kind == "" {
			d.AddError("Project member validation error", "member kind must not be empty")
		}
		if name == "" {
			d.AddError("Project member validation error", "member name must not be empty")
		}
		if role == "" {
			d.AddError("Project member validation error", "member role must not be empty")
		}
	}
}

func projectSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	res := strings.Trim(b.String(), "-")
	for strings.Contains(res, "--") {
		res = strings.ReplaceAll(res, "--", "-")
	}
	if len(res) < 3 {
		res = res + strings.Repeat("0", 3-len(res))
	}
	if len(res) > 40 {
		res = strings.TrimRight(res[:40], "-")
	}
	return res
}

func (r *projectResource) refresh(ctx context.Context, m *projectModel, p client.Project, d *diag.Diagnostics) {
	m.ID = types.StringValue(p.ID)
	m.Name = types.StringValue(p.Name)
	if p.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(p.Description)
	}
	m.StoragePools = stringSetFrom(ctx, p.StoragePools, m.StoragePools.IsNull())
	m.Networks = stringSetFrom(ctx, p.Networks, m.Networks.IsNull())
	if m.Limits != nil || (p.Limits != nil && (p.Limits.Machines != 0 || p.Limits.VCPUs != 0 || p.Limits.MemoryMiB != 0 || p.Limits.DiskGiB != 0)) {
		lim := client.ProjectLimits{}
		if p.Limits != nil {
			lim = *p.Limits
		}
		m.Limits = &projectLimitsModel{
			Machines:  types.Int64Value(int64(lim.Machines)),
			VCPUs:     types.Int64Value(int64(lim.VCPUs)),
			MemoryMiB: types.Int64Value(lim.MemoryMiB),
			DiskGiB:   types.Int64Value(lim.DiskGiB),
		}
	}
	if len(p.Members) > 0 || !m.Members.IsNull() {
		var memberList []projectMemberModel
		for _, mb := range p.Members {
			memberList = append(memberList, projectMemberModel{
				Kind: types.StringValue(mb.Kind),
				Name: types.StringValue(mb.Name),
				Role: types.StringValue(mb.Role),
			})
		}
		if memberList == nil {
			memberList = []projectMemberModel{}
		}
		s, diags := types.SetValueFrom(ctx, memberObjectType, memberList)
		d.Append(diags...)
		m.Members = s
	} else {
		m.Members = types.SetNull(memberObjectType)
	}
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateMembers(ctx, m.Members, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id := projectSlug(m.Name.ValueString())
	p := client.Project{
		ID:           id,
		Name:         m.Name.ValueString(),
		Description:  strOr(m.Description),
		StoragePools: setStrings(ctx, m.StoragePools),
		Networks:     setStrings(ctx, m.Networks),
		Limits:       limitsFromModel(m.Limits),
		Members:      membersFromSet(ctx, m.Members, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.CreateProject(ctx, p); err != nil {
		resp.Diagnostics.AddError("Project not created", err.Error())
		return
	}
	m.ID = types.StringValue(id)
	if fetched, err := r.c.Project(ctx, id); err == nil {
		r.refresh(ctx, &m, fetched, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.c.Project(ctx, m.ID.ValueString())
	if errors.Is(err, client.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Project not read", err.Error())
		return
	}
	r.refresh(ctx, &m, p, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	validateMembers(ctx, plan.Members, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	p := client.Project{
		ID:           id,
		Name:         plan.Name.ValueString(),
		Description:  strOr(plan.Description),
		StoragePools: setStrings(ctx, plan.StoragePools),
		Networks:     setStrings(ctx, plan.Networks),
		Limits:       limitsFromModel(plan.Limits),
		Members:      membersFromSet(ctx, plan.Members, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.UpdateProject(ctx, id, p); err != nil {
		resp.Diagnostics.AddError("Project not updated", err.Error())
		return
	}
	plan.ID = state.ID
	if fetched, err := r.c.Project(ctx, id); err == nil {
		r.refresh(ctx, &plan, fetched, &resp.Diagnostics)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.c.DeleteProject(ctx, m.ID.ValueString()); err != nil && !errors.Is(err, client.ErrNotFound) {
		resp.Diagnostics.AddError("Project not deleted", err.Error())
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

var (
	_ resource.Resource                = (*projectResource)(nil)
	_ resource.ResourceWithConfigure   = (*projectResource)(nil)
	_ resource.ResourceWithImportState = (*projectResource)(nil)
)
