package filespane

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestFilesSelectionActions(t *testing.T) {
	f := NewState(t.TempDir())
	defer f.Close()
	f.Rows = []string{"a", "b", "c"}
	if change := f.ClickPath("a", false, 0, true); !change.Apply || change.Path != "a" || !slices.Equal(f.Picked, []string{"a"}) {
		t.Fatalf("first click: %+v, %v", change, f.Picked)
	}
	f.Selected = "a" // the host applies the returned preview selection
	add := ui.Ctrl
	if toggle, _ := SelectionModifiers(ui.Super); toggle {
		add = ui.Super
	}
	if change := f.ClickPath("c", false, add, true); !change.Apply || change.Path != "c" || !slices.Equal(f.Picked, []string{"a", "c"}) {
		t.Fatalf("add click: %+v, %v", change, f.Picked)
	}
	if change := f.ClickPath("b", false, ui.Shift, true); !change.Apply || !slices.Equal(f.Picked, []string{"b", "c"}) {
		t.Fatalf("range click: %+v, %v", change, f.Picked)
	}
	if got := f.DragPaths("c"); !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("drag selected paths: %v", got)
	}
	if change := f.RightClickPath("a", false, true); !change.Apply || !slices.Equal(f.Picked, []string{"a"}) {
		t.Fatalf("right click: %+v, %v", change, f.Picked)
	}
	if change := f.ClickPath("a", false, add, true); !change.Apply || change.Path != "" || len(f.Picked) != 0 {
		t.Fatalf("toggle off: %+v, %v", change, f.Picked)
	}
}
