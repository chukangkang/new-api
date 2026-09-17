package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

func IsChannelEnabledForGroupModel(group string, modelName string, channelID int) bool {
	if group == "" || modelName == "" || channelID <= 0 {
		return false
	}
	if !common.MemoryCacheEnabled {
		return isChannelEnabledForGroupModelDB(group, modelName, channelID)
	}

	channelSyncLock.RLock()
	defer channelSyncLock.RUnlock()

	if group2model2channels == nil {
		return false
	}

	if isChannelIDInList(group2model2channels[group][modelName], channelID) {
		return true
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized != "" && normalized != modelName {
		return isChannelIDInList(group2model2channels[group][normalized], channelID)
	}
	return false
}

func IsChannelEnabledForAnyGroupModel(groups []string, modelName string, channelID int) bool {
	if len(groups) == 0 {
		return false
	}
	for _, g := range groups {
		if IsChannelEnabledForGroupModel(g, modelName, channelID) {
			return true
		}
	}
	return false
}

func isChannelEnabledForGroupModelDB(group string, modelName string, channelID int) bool {
	var count int64
	err := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ? and enabled = ?", group, modelName, channelID, true).
		Count(&count).Error
	if err == nil && count > 0 {
		return true
	}
	normalized := ratio_setting.FormatMatchingModelName(modelName)
	if normalized == "" || normalized == modelName {
		return false
	}
	count = 0
	err = DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model = ? and channel_id = ? and enabled = ?", group, normalized, channelID, true).
		Count(&count).Error
	return err == nil && count > 0
}

func isChannelIDInList(list []int, channelID int) bool {
	for _, id := range list {
		if id == channelID {
			return true
		}
	}
	return false
}

// GroupHasModelAbility 判断分组内是否存在配置了该模型的渠道（持久性配置，
// 不看渠道启停状态）。用于区分"模型未配置"(404) 与"渠道暂不可用"(503)。
// 直接查 Ability 表而非启用渠道缓存：渠道被禁用时 Ability 行仍在
// （仅删除渠道才删行），此时应报 503 而非 404。
// 仅在渠道选择失败的路径上调用，频率低，DB 查询开销可接受。
// 匹配口径与渠道选择一致：精确模型名 + FormatMatchingModelName 归一化名。
func GroupHasModelAbility(group string, modelName string) bool {
	if group == "" || modelName == "" {
		return false
	}
	return groupHasModelAbilityDB(group, modelName)
}

func groupHasModelAbilityDB(group string, modelName string) bool {
	names := []string{modelName}
	if normalized := ratio_setting.FormatMatchingModelName(modelName); normalized != "" && normalized != modelName {
		names = append(names, normalized)
	}
	var count int64
	err := DB.Model(&Ability{}).
		Where(commonGroupCol+" = ? and model IN ? and enabled = ?", group, names, true).
		Limit(1).
		Count(&count).Error
	return err == nil && count > 0
}
