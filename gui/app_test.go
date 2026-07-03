package main

import "testing"

func TestAdbTargetArgDetection(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "serial", args: []string{"-s", "device-1", "shell", "id"}, want: true},
		{name: "transport id", args: []string{"-t", "3", "shell", "id"}, want: true},
		{name: "usb shortcut", args: []string{"-d", "shell", "id"}, want: true},
		{name: "emulator shortcut", args: []string{"-e", "shell", "id"}, want: true},
		{name: "no target", args: []string{"shell", "id"}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasAdbTargetArg(tc.args); got != tc.want {
				t.Fatalf("hasAdbTargetArg(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestShouldAutoTargetAdbCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "shell is device scoped", args: []string{"shell", "pm", "list", "packages", "-3"}, want: true},
		{name: "install is device scoped", args: []string{"install", "app.apk"}, want: true},
		{name: "devices is global", args: []string{"devices"}, want: false},
		{name: "connect is global", args: []string{"connect", "127.0.0.1:5555"}, want: false},
		{name: "explicit serial still classifies command", args: []string{"-s", "device-1", "shell", "id"}, want: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoTargetAdbCommand(tc.args); got != tc.want {
				t.Fatalf("shouldAutoTargetAdbCommand(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

func TestParseHeartbeatPopupLog(t *testing.T) {
	log := `07-03 16:54:41.343 10961 14880 I com.zjdx.neuralnexus: heartbeat json: {"devId":"864396b5a6bd4ae394d9e0be98629c47","type":1}
07-03 16:54:41.364 10961 14881 I com.zjdx.neuralnexus: ║ Request URL: https://kdzs.zjip.com/ehome/device/heartbeat
07-03 16:54:42.778 10961 10961 I com.zjdx.neuralnexus: newHeartBeat return data :{"data":{"type":"PONG","taskId":null},"errorCode":0,"errorMsg":"success"}
07-03 17:28:42.144 10961 15284 D pretty-Logger: │ ScheduledPopupService 参数信息 [悬浮弹窗]: 悬浮弹窗 已触发弹窗: 测试20260702交付任务2
07-03 17:28:42.157 10961 15284 D pretty-Logger: │ TAG 参数信息 [触发弹窗参数]: 悬浮弹窗 准备弹窗{"content":"测试20260702交付任务1测试20260702交付任务2","planId":74,"title":"测试20260702交付任务2"}
07-03 17:28:42.182 10961 15284 D pretty-Logger: │ ScheduledPopupService 参数信息 [悬浮弹窗]: 悬浮弹窗 scheduledPopupService checkScheduledPopups 触发定时弹窗: 测试20260702交付任务2 at Thu Jul 02 16:23:35 GMT+08:00 2026
07-03 17:28:42.191 10961 10961 D pretty-Logger: │ ADFloatWindowService 参数信息 [悬浮弹窗popplan createFloatWindow]: 创建悬浮弹窗===
07-03 17:28:42.204 10961 15284 D pretty-Logger: │ ScheduledPopupService 参数信息 [悬浮弹窗]: 悬浮弹窗 已安排下次弹窗检查: 2026-07-03 17:58:42
07-03 17:28:42.764 10961 10961 D pretty-Logger: │ ADFloatWindowService 参数信息 [悬浮弹窗popplan获取悬浮弹窗]: 悬浮弹窗已获取焦点
`

	heartbeats, popups, nextCheck := parseHeartbeatPopupLog(log)
	if len(heartbeats) != 2 {
		t.Fatalf("heartbeats = %d, want 2", len(heartbeats))
	}
	if heartbeats[0].Direction != "request" || heartbeats[0].DevID != "864396b5a6bd4ae394d9e0be98629c47" {
		t.Fatalf("unexpected heartbeat request: %+v", heartbeats[0])
	}
	if heartbeats[1].Direction != "response" || heartbeats[1].ResultType != "PONG" {
		t.Fatalf("unexpected heartbeat response: %+v", heartbeats[1])
	}
	if len(popups) != 1 {
		t.Fatalf("popups = %d, want 1", len(popups))
	}
	popup := popups[0]
	if popup.FocusTime != "07-03 17:28:42.764" || popup.CreateTime != "07-03 17:28:42.191" {
		t.Fatalf("unexpected popup times: %+v", popup)
	}
	if popup.Title != "测试20260702交付任务2" || popup.PlanID != "74" {
		t.Fatalf("unexpected popup payload: %+v", popup)
	}
	if nextCheck != "2026-07-03 17:58:42" {
		t.Fatalf("nextCheck = %q, want %q", nextCheck, "2026-07-03 17:58:42")
	}
}
