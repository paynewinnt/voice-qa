package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"voice-qa/internal/perfetto"
)

type perfRecordGroup struct {
	key  string
	runs []perfetto.LaunchResult
}

func perfRecordGroupKey(run perfetto.LaunchResult) string {
	categories := append([]string{}, run.LaunchCategories...)
	sort.Strings(categories)
	key, _ := json.Marshal([]any{run.Serial, run.PackageName, componentIdentity(run.Component, run.PackageName),
		run.LaunchAction, categories, run.LaunchDataURI, run.ForceStopBefore, run.ClearDataBefore})
	return string(key)
}

func perfRecordGroupLabel(run perfetto.LaunchResult) string {
	return fmt.Sprintf("%s | %s | force-stop=%t, pm clear=%t", run.Serial, perfRunLaunchCommand(run), run.ForceStopBefore, run.ClearDataBefore)
}

func groupPerfRecords(runs []perfetto.LaunchResult) []perfRecordGroup {
	var groups []perfRecordGroup
	indices := make(map[string]int)
	for _, run := range runs {
		key := perfRecordGroupKey(run)
		index, exists := indices[key]
		if !exists {
			index = len(groups)
			indices[key] = index
			groups = append(groups, perfRecordGroup{key: key})
		}
		groups[index].runs = append(groups[index].runs, run)
	}
	return groups
}

func buildPerfAggregateReport(batch PerfBatchResult, reportPath string, metrics perfReportMetrics) string {
	groups := groupPerfRecords(batch.Runs)
	if len(groups) == 0 {
		return "启动性能测试聚合报告\n没有可统计的测试记录。\n"
	}
	if len(groups) == 1 {
		return buildPerfGroupReport(batch, reportPath, metrics)
	}
	var report strings.Builder
	fmt.Fprintf(&report, "启动性能测试聚合报告\n按设备、应用、Component、Intent 与启动方式分为 %d 组，不计算跨组平均值。\n\n", len(groups))
	for index, group := range groups {
		part := PerfBatchResult{Runs: group.runs, ResultDir: batch.ResultDir, Count: len(group.runs)}
		for _, run := range group.runs {
			if run.Success {
				part.Successes++
			}
		}
		fmt.Fprintf(&report, "分组 %d/%d\n%s\n", index+1, len(groups), buildPerfGroupReport(part, reportPath, metrics))
	}
	return report.String()
}
