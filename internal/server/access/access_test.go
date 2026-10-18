package access

import (
	"slices"
	"testing"

	"opensignage/internal/server/core"
)

func restricted(zones map[string]string) *core.Principal {
	return &core.Principal{UserID: "me", Role: core.RoleOperator, Scope: core.ScopeZones, Zones: zones}
}

func TestDecide(t *testing.T) {
	p := restricted(map[string]string{"bar2": core.AccessEdit, "bar3": core.AccessView})
	cases := []struct {
		name      string
		zones     []string
		global    bool
		createdBy string
		want      Rights
	}{
		{"only my edit zone", []string{"bar2"}, false, "", Rights{View: true, Edit: true}},
		{"also a zone I only see", []string{"bar2", "bar3"}, false, "", Rights{View: true, Shared: true}},
		{"also a foreign zone", []string{"bar1", "bar2"}, false, "", Rights{View: true, Shared: true}},
		{"only a zone I see", []string{"bar3"}, false, "", Rights{View: true, Shared: true}},
		{"foreign zone only", []string{"bar1"}, false, "", Rights{}},
		{"global rule", nil, true, "", Rights{View: true, Shared: true}},
		{"global and my zone", []string{"bar2"}, true, "", Rights{View: true, Shared: true}},
		{"mine, runs nowhere", nil, false, "me", Rights{View: true, Edit: true}},
		{"mine, runs in my zone", []string{"bar2"}, false, "me", Rights{View: true, Edit: true}},
		{"mine, runs in a foreign zone", []string{"bar1"}, false, "me", Rights{View: true, Shared: true}},
		{"someone else's, runs nowhere", nil, false, "other", Rights{}},
	}
	for _, c := range cases {
		if got := Decide(p, c.zones, c.global, c.createdBy); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	all := Rights{View: true, Edit: true}
	if got := Decide(&core.Principal{Role: core.RoleOperator, Scope: core.ScopeAll}, []string{"bar1"}, true, ""); got != all {
		t.Errorf("operator of all zones: %+v", got)
	}
	if got := Decide(&core.Principal{Role: core.RoleAdmin, Scope: core.ScopeZones}, nil, false, ""); got != all {
		t.Errorf("admins are never limited: %+v", got)
	}
	if got := Decide(&core.Principal{Role: core.RoleUser, Zones: map[string]string{"bar2": core.AccessEdit}}, []string{"bar2"}, false, ""); got != (Rights{}) {
		t.Errorf("users have no content rights: %+v", got)
	}
}

func TestPresentationsOfPlaylist(t *testing.T) {
	doc := `{"items":[
		{"id":"a","kind":"presentation","refId":"p1"},
		{"id":"b","kind":"slide","refId":"s1","presentationId":"p2","cells":[null,{"kind":"slide","refId":"s9","presentationId":"p3"},{"kind":"media","refId":"m1"}]},
		{"id":"c","kind":"media","refId":"m2"},
		{"id":"d","kind":"presentation","refId":"p1"}]}`
	if got := Presentations(doc); !slices.Equal(got, []string{"p1", "p2", "p3"}) {
		t.Fatalf("presentations %v", got)
	}
	if Presentations("not json") != nil {
		t.Fatal("broken documents contain nothing")
	}
}

func TestRightsHelpers(t *testing.T) {
	p := restricted(map[string]string{"bar2": core.AccessEdit, "bar3": core.AccessView})
	p.Media, p.Products, p.Devices = core.MediaUpload, core.ProductsView, true
	if !p.CanMedia(core.MediaUse) || !p.CanMedia(core.MediaUpload) || p.CanMedia(core.MediaManage) {
		t.Error("media rights include the lower ones only")
	}
	if p.CanEditProducts() || p.CanCreateContent() || !p.CanZone("bar2") || p.CanZone("bar3") || p.CanViewZone("bar1") {
		t.Error("products, content and zone rights")
	}
	if !p.CanControlDevice("bar2") || p.CanControlDevice("bar3") {
		t.Error("displays only in zones with edit access")
	}
	if got := p.EditZones(); !slices.Equal(got, []string{"bar2"}) {
		t.Errorf("edit zones %v", got)
	}
	op := &core.Principal{Role: core.RoleOperator, Scope: core.ScopeAll}
	if !op.CanMedia(core.MediaManage) || !op.CanEditProducts() || !op.CanControlDevice("x") || op.EditZones() != nil || !op.CanViewZone("x") {
		t.Error("operators of all zones keep every right")
	}
	user := &core.Principal{Role: core.RoleUser, Zones: map[string]string{"bar2": core.AccessEdit}}
	if user.CanMedia(core.MediaUse) || user.CanEditProducts() || !user.CanZone("bar2") || user.CanZone("bar1") {
		t.Error("users: live operation in their zones only")
	}
}
