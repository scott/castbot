package connect

import "testing"

func TestPanelsStayOutOfTheProxyCatalog(t *testing.T) {
	panels := Panels()
	if len(panels) != 3 || panels[0].ID != GitHub || panels[1].ID != DigitalOcean || panels[2].ID != Shell {
		t.Fatalf("%v", panels)
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
