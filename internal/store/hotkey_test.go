package store

import "testing"

func TestNormalizeHotkeyCombo(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"cmd+shift+r", "shift+cmd+r"},
		{"CMD+Alt+F5", "opt+cmd+f5"},
		{"ctrl+opt+shift+cmd+space", "ctrl+opt+shift+cmd+space"},
		{"Fn+Space", "fn+space"},
		{"fn+ctrl+k", "fn+ctrl+k"},
		{"command+shift+ctrl+k", "ctrl+shift+cmd+k"},
		{"cmd+r", "cmd+r"},
	}
	for _, c := range cases {
		got, err := NormalizeHotkeyCombo(c.in)
		if err != nil {
			t.Fatalf("NormalizeHotkeyCombo(%q) 意外报错: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("NormalizeHotkeyCombo(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeHotkeyComboRejects(t *testing.T) {
	bad := []string{"r", "cmd", "cmd+a+b", "cmd+home", "cmd+", ""}
	for _, in := range bad {
		if _, err := NormalizeHotkeyCombo(in); err == nil {
			t.Fatalf("NormalizeHotkeyCombo(%q) 应当报错，却通过了", in)
		}
	}
}

func TestSplitHotkeyCombo(t *testing.T) {
	combo, err := NormalizeHotkeyCombo("cmd+shift+r")
	if err != nil {
		t.Fatal(err)
	}
	mods, key, err := SplitHotkeyCombo(combo)
	if err != nil {
		t.Fatal(err)
	}
	if key != "r" {
		t.Fatalf("key = %q, 想要 r", key)
	}
	if len(mods) != 2 || mods[0] != "shift" || mods[1] != "cmd" {
		t.Fatalf("mods = %v, 想要 [shift cmd]", mods)
	}
}

func TestValidateHotkeys(t *testing.T) {
	cfg := HotkeyConfig{
		Enabled: true,
		Items: []HotkeyItem{
			{ID: "a", Name: "重启 frpc", Combo: "Cmd+Shift+R", Action: HotkeyActionRun, Command: "ls"},
			{ID: "b", Name: "SSH 登录", Action: HotkeyActionITerm, Command: "ssh root@1.2.3.4"},
		},
	}
	if err := ValidateHotkeys(&cfg); err != nil {
		t.Fatalf("合法配置被拒: %v", err)
	}
	if cfg.Items[0].Combo != "shift+cmd+r" {
		t.Fatalf("组合键未被规范化: %q", cfg.Items[0].Combo)
	}
	if cfg.Items[1].Combo != "" {
		t.Fatalf("空组合键应当保留为空，got %q", cfg.Items[1].Combo)
	}
	if cfg.PaletteCombo != DefaultPaletteCombo {
		t.Fatalf("默认命令面板触发键 = %q, 想要 %q", cfg.PaletteCombo, DefaultPaletteCombo)
	}
}

func TestValidateHotkeysRejects(t *testing.T) {
	base := func() HotkeyConfig {
		return HotkeyConfig{
			Enabled: true,
			Items: []HotkeyItem{
				{ID: "a", Name: "一", Combo: "cmd+r", Action: HotkeyActionRun, Command: "ls"},
				{ID: "b", Name: "二", Combo: "cmd+t", Action: HotkeyActionRun, Command: "ls"},
			},
		}
	}

	dupID := base()
	dupID.Items[1].ID = "a"
	if err := ValidateHotkeys(&dupID); err == nil {
		t.Fatal("重复 ID 应当报错")
	}

	dupCombo := base()
	dupCombo.Items[1].Combo = "cmd+r"
	if err := ValidateHotkeys(&dupCombo); err == nil {
		t.Fatal("重复组合键应当报错")
	}

	emptyCmd := base()
	emptyCmd.Items[0].Command = ""
	if err := ValidateHotkeys(&emptyCmd); err == nil {
		t.Fatal("空命令应当报错")
	}

	badAction := base()
	badAction.Items[0].Action = "bogus"
	if err := ValidateHotkeys(&badAction); err == nil {
		t.Fatal("非法动作应当报错")
	}

	paletteConflict := base()
	paletteConflict.PaletteCombo = "cmd+r"
	if err := ValidateHotkeys(&paletteConflict); err == nil {
		t.Fatal("命令面板触发键和普通快捷键冲突应当报错")
	}
}

func TestHotkeysRoundtrip(t *testing.T) {
	isolateHome(t)

	// 文件不存在时返回空配置，不报错。
	cfg, err := LoadHotkeys()
	if err != nil {
		t.Fatalf("LoadHotkeys 意外报错: %v", err)
	}
	if cfg.Enabled || len(cfg.Items) != 0 {
		t.Fatalf("空配置应当为 false + 空列表, got %+v", cfg)
	}

	want := HotkeyConfig{
		Enabled:      true,
		OrderVersion: 1,
		Items: []HotkeyItem{
			{ID: "a", Name: "一", Combo: "cmd+r", Action: HotkeyActionRun, Command: "ls -la"},
		},
	}
	if err := SaveHotkeys(want); err != nil {
		t.Fatalf("SaveHotkeys 失败: %v", err)
	}
	got, err := LoadHotkeys()
	if err != nil {
		t.Fatalf("LoadHotkeys 失败: %v", err)
	}
	if !got.Enabled || got.OrderVersion != 1 || len(got.Items) != 1 || got.Items[0].Command != "ls -la" {
		t.Fatalf("roundtrip 不一致: %+v", got)
	}
}
