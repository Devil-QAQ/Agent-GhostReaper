package rings

import (
	"context"
	"errors"
	"strconv"
)

// Each task and each switch has its own audited setting. Saving one cannot
// overwrite the other or implicitly enable automation in a different task.
type ApprovalAutomation struct {
	AutoApprove bool `json:"auto_approve"`
	AutoJoin    bool `json:"auto_join"`
}

func (s *Store) ApprovalAutomationFlag(ctx context.Context, task, flag string) (bool, error) {
	if task == "" || (flag != "auto_approve" && flag != "auto_join") {
		return false, errors.New("自动处理设置无效")
	}
	entry, ok, err := s.Setting(ctx, "roe.automation."+task+"."+flag)
	if err != nil || !ok {
		return false, err
	}
	value, err := strconv.ParseBool(entry.Value)
	if err != nil {
		return false, errors.New("自动处理设置无效")
	}
	return value, nil
}

func (s *Store) SetApprovalAutomationFlag(ctx context.Context, task, flag string, enabled bool) error {
	if task == "" || (flag != "auto_approve" && flag != "auto_join") {
		return errors.New("自动处理设置无效")
	}
	return s.SetSetting(ctx, "roe.automation."+task+"."+flag, strconv.FormatBool(enabled), "creator")
}

func (s *Store) ApprovalAutomation(ctx context.Context, task string) (ApprovalAutomation, error) {
	var result ApprovalAutomation
	var err error
	result.AutoApprove, err = s.ApprovalAutomationFlag(ctx, task, "auto_approve")
	if err != nil {
		return result, err
	}
	result.AutoJoin, err = s.ApprovalAutomationFlag(ctx, task, "auto_join")
	return result, err
}
