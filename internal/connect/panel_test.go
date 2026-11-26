package connect

import "testing"

func TestPanelsStayOutOfTheProxyCatalog(t *testing.T) {
	panels := Panels()
	catalog := Catalog()
	if len(panels) != len(catalog)+1 || len(catalog) < 2 || panels[0].ID != GitHub || panels[1].ID != DigitalOcean || panels[len(panels)-1].ID != Shell {
		t.Fatalf("panels %d catalog %d", len(panels), len(catalog))
	}
	for i, entry := range catalog {
		if panels[i].ID != entry.ID || !panels[i].NeedsSecret {
			t.Fatalf("panel %d %+v", i, panels[i])
		}
	}
	if _, ok := Lookup(Shell); ok {
		t.Fatal("shell joined the proxy catalog")
	}
	if Tools(Shell) != nil {
		t.Fatal("shell has proxy tools")
	}
	shell, ok := PanelLookup(Shell)
	if !ok || shell.NeedsSecret || len(shell.Tools) != 9 {
		t.Fatalf("%+v", shell)
	}
	if DefaultOn(GitHub) || DefaultOn(DigitalOcean) || !DefaultOn(Shell) {
		t.Fatal("defaults")
	}
}
