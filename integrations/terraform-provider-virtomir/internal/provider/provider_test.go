package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestProviderResources(t *testing.T) {
	p := virtomirProvider{version: "test"}
	resources := p.Resources(context.Background())
	if len(resources) != 5 {
		t.Fatalf("expected 5 resources, got %d", len(resources))
	}

	names := make(map[string]bool)
	for _, fn := range resources {
		r := fn()
		var resp resource.MetadataResponse
		r.Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "virtomir"}, &resp)
		names[resp.TypeName] = true
	}

	expected := []string{
		"virtomir_machine",
		"virtomir_network",
		"virtomir_pool",
		"virtomir_project",
		"virtomir_placement_rule",
	}
	for _, exp := range expected {
		if !names[exp] {
			t.Errorf("missing expected resource: %s", exp)
		}
	}
}

func TestProjectResourceSchema(t *testing.T) {
	r := newProject()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics error: %v", resp.Diagnostics)
	}

	attrs := resp.Schema.Attributes
	for _, attrName := range []string{"id", "name", "description", "storage_pools", "networks", "limits", "members"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing project attribute %s", attrName)
		}
	}

	if !attrs["id"].IsComputed() {
		t.Errorf("expected id to be computed")
	}
	if !attrs["name"].IsRequired() {
		t.Errorf("expected name to be required")
	}
	if !attrs["storage_pools"].IsOptional() {
		t.Errorf("expected storage_pools to be optional")
	}
	if !attrs["networks"].IsOptional() {
		t.Errorf("expected networks to be optional")
	}
	if !attrs["limits"].IsOptional() {
		t.Errorf("expected limits to be optional")
	}
	if !attrs["members"].IsOptional() {
		t.Errorf("expected members to be optional")
	}
}

func TestPlacementRuleResourceSchema(t *testing.T) {
	r := newPlacementRule()
	var resp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("Schema diagnostics error: %v", resp.Diagnostics)
	}

	attrs := resp.Schema.Attributes
	for _, attrName := range []string{"id", "name", "kind", "strength", "enabled", "domains", "hosts", "exclude"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing placement_rule attribute %s", attrName)
		}
	}

	if !attrs["id"].IsComputed() {
		t.Errorf("expected id to be computed")
	}
	if !attrs["name"].IsRequired() {
		t.Errorf("expected name to be required")
	}
	if !attrs["kind"].IsRequired() {
		t.Errorf("expected kind to be required")
	}
	if !attrs["strength"].IsComputed() || !attrs["strength"].IsOptional() {
		t.Errorf("expected strength to be optional + computed")
	}
	if !attrs["enabled"].IsComputed() || !attrs["enabled"].IsOptional() {
		t.Errorf("expected enabled to be optional + computed")
	}
	if !attrs["domains"].IsRequired() {
		t.Errorf("expected domains to be required")
	}
	if !attrs["hosts"].IsOptional() {
		t.Errorf("expected hosts to be optional")
	}
	if !attrs["exclude"].IsComputed() || !attrs["exclude"].IsOptional() {
		t.Errorf("expected exclude to be optional + computed")
	}
}

func TestProjectSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"dev", "dev"},
		{"Development", "development"},
		{"Team Alpha", "team-alpha"},
		{"Engineering & Ops", "engineering-ops"},
		{"ab", "ab0"},
		{"a", "a00"},
		{"--test--slug--", "test-slug"},
	}
	for _, c := range cases {
		got := projectSlug(c.in)
		if got != c.want {
			t.Errorf("projectSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLimitsFromModel(t *testing.T) {
	m := &projectLimitsModel{
		Machines:  types.Int64Value(3),
		VCPUs:     types.Int64Value(6),
		MemoryMiB: types.Int64Value(4096),
		DiskGiB:   types.Int64Value(50),
	}
	lim := limitsFromModel(m)
	if lim == nil {
		t.Fatal("expected non-nil limits")
	}
	if lim.Machines != 3 || lim.VCPUs != 6 || lim.MemoryMiB != 4096 || lim.DiskGiB != 50 {
		t.Errorf("unexpected limits: %+v", lim)
	}

	limNil := limitsFromModel(nil)
	if limNil != nil {
		t.Errorf("expected nil limits for nil model, got: %+v", limNil)
	}
}

func TestValidateMembers(t *testing.T) {
	ctx := context.Background()

	validMembers := []projectMemberModel{
		{Kind: types.StringValue("user"), Name: types.StringValue("alice"), Role: types.StringValue("admin")},
	}
	s, diags := types.SetValueFrom(ctx, memberObjectType, validMembers)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var d diag.Diagnostics
	validateMembers(ctx, s, &d)
	if d.HasError() {
		t.Errorf("expected no errors for valid members, got: %v", d)
	}

	invalidMembers := []projectMemberModel{
		{Kind: types.StringValue(""), Name: types.StringValue("alice"), Role: types.StringValue("admin")},
	}
	s2, diags := types.SetValueFrom(ctx, memberObjectType, invalidMembers)
	if diags.HasError() {
		t.Fatal(diags)
	}
	var d2 diag.Diagnostics
	validateMembers(ctx, s2, &d2)
	if !d2.HasError() {
		t.Error("expected errors for empty member kind")
	}
}
