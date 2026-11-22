package guikit

import (
	"image"
	"testing"
)

func TestNavSidebarIsTheSidebarColumn(t *testing.T) {
	th := NewTheme()
	n := &Nav{Title: "Isotope System", Note: "Isotope v1.0.0", Items: []NavItem{
		{Name: "Updates"}, {Name: "Extensions"}, {Name: "Users", Later: true},
	}, Current: 1}
	dims := th.NavSidebar(testContext(1000, 600), n)
	if dims.Size != image.Pt(236, 600) {
		t.Fatalf("NavSidebar is %v, want 236x600 like Sidebar", dims.Size)
	}
	if got := n.Clicked(testContext(1000, 600)); got != -1 {
		t.Fatalf("no click, but Clicked says %d", got)
	}
	if n.Row(2) == nil {
		t.Fatal("no clickable for the last row")
	}
}

func TestNavGrowsWithItsItems(t *testing.T) {
	n := &Nav{Items: []NavItem{{Name: "A"}}}
	_ = n.Row(0)
	n.Items = append(n.Items, NavItem{Name: "B"})
	if n.Row(1) == nil {
		t.Fatal("a row added later has no clickable")
	}
}
