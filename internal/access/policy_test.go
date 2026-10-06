package access

import (
	"reflect"
	"testing"
)

func TestAllowAll(t *testing.T) {
	p := AllowAll()
	if !p.Allowed("ANY") || p.Projects() != nil {
		t.Fatalf("AllowAll must allow everything and list nil, got %v", p.Projects())
	}
}

func TestProjects(t *testing.T) {
	p := Projects("pay", " DOSD ", "", "PAY")
	if !p.Allowed("PAY") || !p.Allowed("dosd") || p.Allowed("HR") {
		t.Fatal("Projects must match keys case-insensitively and reject others")
	}
	if got := p.Projects(); !reflect.DeepEqual(got, []string{"DOSD", "PAY"}) {
		t.Fatalf("Projects() = %v, want [DOSD PAY]", got)
	}
}

func TestProjects_EmptyAllowsNothing(t *testing.T) {
	p := Projects()
	if p.Allowed("PAY") {
		t.Fatal("an empty project list must allow nothing")
	}
	if got := p.Projects(); got == nil || len(got) != 0 {
		t.Fatalf("Projects() = %#v, want an empty non-nil list", got)
	}
}

func TestIntersect(t *testing.T) {
	a := Projects("PAY", "DOSD")
	b := Projects("DOSD", "HR")
	if got := Intersect(a, b).Projects(); !reflect.DeepEqual(got, []string{"DOSD"}) {
		t.Fatalf("Intersect = %v, want [DOSD]", got)
	}
	if got := Intersect(AllowAll(), a).Projects(); !reflect.DeepEqual(got, []string{"DOSD", "PAY"}) {
		t.Fatalf("Intersect(AllowAll, a) = %v, want a", got)
	}
	if got := Intersect(a, AllowAll()).Projects(); !reflect.DeepEqual(got, []string{"DOSD", "PAY"}) {
		t.Fatalf("Intersect(a, AllowAll) = %v, want a", got)
	}
	if Intersect(AllowAll(), AllowAll()).Projects() != nil {
		t.Fatal("Intersect of two AllowAll must stay unrestricted")
	}
	if Intersect(a, Projects("HR")).Allowed("PAY") {
		t.Fatal("disjoint lists must allow nothing")
	}
}
