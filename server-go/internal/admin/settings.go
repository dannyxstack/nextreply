package admin

// 阶段 3：运行配置页面。值的含义、校验和生效逻辑都在 settings 包里，这里只负责展示和提交。

import (
	"context"
	"errors"
	"net/http"

	"github.com/nextreply/server/internal/ai"
	"github.com/nextreply/server/internal/settings"
)

type SettingRow struct {
	settings.Field
	Current    string
	Default    string
	Overridden bool
	Options    []string // 下拉框的可选值；为空时用输入框
}

type SettingGroup struct {
	Name string
	Rows []SettingRow
}

type SettingsPage struct {
	Groups  []SettingGroup
	CostCap settings.CostCap
}

func (a *Admin) settingsData(r *http.Request) (any, error) {
	page := SettingsPage{}
	var err error
	if page.CostCap, err = a.settings.CostCap(r.Context()); err != nil {
		return nil, err
	}
	index := map[string]int{}
	for _, f := range settings.Fields {
		row := SettingRow{Field: f, Current: a.settings.Get(f.Key), Default: a.settings.Default(f.Key), Overridden: a.settings.Overridden(f.Key)}
		switch f.Kind {
		case settings.KindModel:
			row.Options = ai.KnownModels()
		case settings.KindEffort:
			row.Options = settings.Efforts
		case settings.KindChoice:
			row.Options = f.Choices
		case settings.KindBool:
			row.Options = []string{"true", "false"}
		}
		i, ok := index[f.Group]
		if !ok {
			i = len(page.Groups)
			index[f.Group] = i
			page.Groups = append(page.Groups, SettingGroup{Name: f.Group})
		}
		page.Groups[i].Rows = append(page.Groups[i].Rows, row)
	}
	return page, nil
}

// updateSettings 保存整张表单：留空表示使用默认值。全部合法才写入，写入后立即生效。
func (a *Admin) updateSettings(ctx context.Context, r *http.Request) (string, map[string]any, string, error) {
	var changes []settings.Change
	for _, f := range settings.Fields {
		if _, ok := r.PostForm[f.Key]; ok {
			changes = append(changes, settings.Change{Key: f.Key, Value: r.PostFormValue(f.Key)})
		}
	}
	why, err := reason(r, false)
	if err != nil {
		return "settings", nil, "", err
	}
	diff, err := a.settings.Apply(ctx, changes, actor(r))
	var ve *settings.ValidationError
	if errors.As(err, &ve) {
		return "settings", nil, "", actionError(ve.Msg)
	}
	if err != nil {
		return "settings", nil, "", err
	}
	if len(diff) == 0 {
		return "settings", nil, "", actionError("没有修改")
	}
	detail := map[string]any{"reason": why}
	for k, v := range diff {
		detail[k] = v[0] + " → " + v[1]
	}
	return "settings", detail, "配置已保存并生效", nil
}
