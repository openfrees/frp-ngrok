package hotkey

import (
	"reflect"
	"testing"
	"time"

	"github.com/openfrees/frp-ngrok/internal/store"
)

type fakeEngine struct {
	items  []store.HotkeyItem
	onFire func(int)
	stops  int
}

func (e *fakeEngine) register(items []store.HotkeyItem, onFire func(int)) error {
	e.items = append([]store.HotkeyItem(nil), items...)
	e.onFire = onFire
	return nil
}

func (e *fakeEngine) stop() { e.stops++ }

func TestManagerRegistersCommandItemsAndPaletteTrigger(t *testing.T) {
	eng := &fakeEngine{}
	ran := make(chan string, 1)
	var palette [][]store.HotkeyItem
	m := newManagerWithEngine(
		func(item store.HotkeyItem) { ran <- item.ID },
		func(items []store.HotkeyItem, _ Dispatcher) {
			palette = append(palette, append([]store.HotkeyItem(nil), items...))
		},
		func() engine { return eng },
	)

	cfg := store.HotkeyConfig{
		Enabled:      true,
		PaletteCombo: "Fn+Space",
		Items: []store.HotkeyItem{
			{ID: "restart", Name: "重启", Combo: "cmd+r", Action: store.HotkeyActionRun, Command: "echo restart"},
			{ID: "logs", Name: "日志", Action: store.HotkeyActionRun, Command: "echo logs"},
			{ID: "deploy", Name: "部署", Combo: "cmd+d", Action: store.HotkeyActionRun, Command: "echo deploy"},
		},
	}
	if err := m.Apply(cfg); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}

	gotCombos := []string{eng.items[0].Combo, eng.items[1].Combo, eng.items[2].Combo}
	wantCombos := []string{"cmd+r", "cmd+d", store.DefaultPaletteCombo}
	if !reflect.DeepEqual(gotCombos, wantCombos) {
		t.Fatalf("注册组合键 = %v, 想要 %v", gotCombos, wantCombos)
	}

	eng.onFire(1)
	select {
	case got := <-ran:
		if got != "deploy" {
			t.Fatalf("普通快捷键派发 = %q, 想要 deploy", got)
		}
	case <-time.After(time.Second):
		t.Fatal("普通快捷键没有派发")
	}

	eng.onFire(2)
	if len(palette) != 1 || len(palette[0]) != 3 || palette[0][1].ID != "logs" {
		t.Fatalf("命令面板拿到的条目不对: %+v", palette)
	}
}

func TestManagerRegistersPaletteWhenNoCommandItems(t *testing.T) {
	eng := &fakeEngine{}
	m := newManagerWithEngine(nil, func([]store.HotkeyItem, Dispatcher) {}, func() engine { return eng })

	if err := m.Apply(store.HotkeyConfig{Enabled: true}); err != nil {
		t.Fatalf("Apply 失败: %v", err)
	}
	if len(eng.items) != 1 || eng.items[0].Combo != store.DefaultPaletteCombo {
		t.Fatalf("空命令列表也应注册命令面板触发键，got %+v", eng.items)
	}
}
