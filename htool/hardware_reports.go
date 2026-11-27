package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type bootCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}

type bootReport struct {
	BootID  string      `json:"bootId"`
	Checked time.Time   `json:"checked"`
	Checks  []bootCheck `json:"checks"`
}

var bootIDPattern = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
var emmcPattern = regexp.MustCompile(`^mmcblk[0-9]+$`)
var nvmePattern = regexp.MustCompile(`^nvme[0-9]+n[0-9]+$`)

func (m *hardwareManager) loadReports() ([]bootReport, error) {
	var reports []bootReport
	err := readHardwareState(filepath.Join(m.stateDir, "boot-checks.json"), &reports)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err == nil && len(reports) > 3 {
		err = errors.New("invalid boot history")
	}
	return reports, err
}

func (m *hardwareManager) reports() hardwareReply {
	m.reportMu.Lock()
	defer m.reportMu.Unlock()
	history, err := m.loadReports()
	if err != nil {
		return hardwareError(503, "无法读取启动检查记录。")
	}
	return hardwareReply{Reports: history, Checking: m.recording, Error: m.reportError}
}

func (m *hardwareManager) recordBoot() {
	defer func() {
		m.reportMu.Lock()
		m.recording = false
		m.reportMu.Unlock()
	}()
	bootID := textFile("/proc/sys/kernel/random/boot_id", 80)
	if !bootIDPattern.MatchString(bootID) {
		m.bootReportFailure(errors.New("invalid boot ID"))
		return
	}
	m.reportMu.Lock()
	history, err := m.loadReports()
	m.reportMu.Unlock()
	if err != nil {
		m.bootReportFailure(err)
		return
	}
	for _, existing := range history {
		if existing.BootID == bootID {
			return
		}
	}

	// Keep the last complete report visible while slow devices enumerate.
	deadline := time.Now().Add(18 * time.Second)
	time.Sleep(5 * time.Second)
	checks := m.systemChecks()
	for delay := time.Until(deadline); checksNeedRetry(checks) && delay > 0; delay = time.Until(deadline) {
		time.Sleep(min(3*time.Second, delay))
		checks = m.systemChecks()
	}
	report := bootReport{BootID: bootID, Checked: time.Now().UTC(), Checks: checks}
	for _, c := range report.Checks {
		log.Printf("boot-check boot=%s check=%q state=%s detail=%q", bootID, c.Name, c.State, c.Detail)
	}
	history = retainBootReport(history, report)
	m.reportMu.Lock()
	err = saveHardwareState(filepath.Join(m.stateDir, "boot-checks.json"), history)
	m.reportMu.Unlock()
	if err != nil {
		m.bootReportFailure(err)
	}
}

func checksNeedRetry(checks []bootCheck) bool {
	for _, check := range checks {
		switch check.State {
		case "error", "missing", "partial", "timeout", "unknown":
			return true
		case "enumerated":
			if check.Name == "蓝牙" {
				return true
			}
		}
	}
	return false
}

func retainBootReport(history []bootReport, report bootReport) []bootReport {
	for _, existing := range history {
		if existing.BootID == report.BootID {
			return history
		}
	}
	history = append([]bootReport{report}, history...)
	return history[:min(len(history), 3)]
}

func (m *hardwareManager) bootReportFailure(err error) {
	log.Printf("boot-check: %v", err)
	m.reportMu.Lock()
	defer m.reportMu.Unlock()
	m.reportError = "本次启动检查未能保存。"
}

func interfaceChecks() (bootCheck, bootCheck) {
	ethernet := bootCheck{"以太网", "missing", "未识别到物理以太网口"}
	wifi := bootCheck{"Wi-Fi", "missing", "未识别到无线网络接口"}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		ethernet.State, ethernet.Detail = "error", "无法读取网络接口"
		wifi.State, wifi.Detail = ethernet.State, ethernet.Detail
		return ethernet, wifi
	}
	var wired, wireless []string
	for _, entry := range entries {
		name := entry.Name()
		sys := filepath.Join("/sys/class/net", name)
		_, w := os.Stat(filepath.Join(sys, "wireless"))
		_, phy := os.Stat(filepath.Join(sys, "phy80211"))
		if w == nil || phy == nil {
			wireless = append(wireless, name)
			if textFile(filepath.Join(sys, "operstate"), 24) == "up" {
				wifi.State = "ready"
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(sys, "device")); err != nil ||
			textFile(filepath.Join(sys, "type"), 16) != "1" {
			continue
		}
		link := "链路状态未知"
		switch textFile(filepath.Join(sys, "carrier"), 4) {
		case "1":
			link = "已连接"
		case "0":
			link = "未检测到链路"
		}
		wired = append(wired, name+" "+link)
	}
	if len(wired) > 0 {
		ethernet.State, ethernet.Detail = "ready", summarize(wired)
		if len(wired) < 2 {
			ethernet.State = "partial"
			ethernet.Detail += "，少于预期的两个网口"
		}
	}
	if len(wireless) > 0 {
		wifi.Detail = summarize(wireless)
		if wifi.State != "ready" {
			wifi.State = "enumerated"
			wifi.Detail += "，网络未连接"
		}
	}
	return ethernet, wifi
}

func summarize(values []string) string {
	if len(values) > 8 {
		values = values[:8]
	}
	text := strings.Join(values, "；")
	runes := []rune(text)
	if len(runes) > 200 {
		return string(runes[:200]) + "…"
	}
	return text
}

func storageChecks() (bootCheck, bootCheck, bootCheck) {
	emmc := bootCheck{"eMMC", "missing", "未识别到 eMMC"}
	nvme := bootCheck{"NVMe", "optional-absent", "未安装或未识别到选装 NVMe"}
	root := bootCheck{"系统根分区", "unknown", "未确认根分区来源"}
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		emmc.State, emmc.Detail = "error", "无法读取块设备"
		nvme.State, nvme.Detail = emmc.State, emmc.Detail
	}
	var emmcNames, nvmeNames []string
	for _, entry := range entries {
		name := entry.Name()
		if emmcPattern.MatchString(name) && textFile("/sys/class/block/"+name+"/device/type", 16) == "MMC" {
			emmcNames = append(emmcNames, name)
		}
		if nvmePattern.MatchString(name) {
			nvmeNames = append(nvmeNames, name)
		}
	}
	if len(emmcNames) > 0 {
		emmc.State, emmc.Detail = "ready", summarize(emmcNames)
	}
	if len(nvmeNames) > 0 {
		nvme.State, nvme.Detail = "ready", summarize(nvmeNames)
	}
	for _, line := range strings.Split(textFile("/proc/self/mountinfo", 65536), "\n") {
		before, after, ok := strings.Cut(line, " - ")
		fields, fs := strings.Fields(before), strings.Fields(after)
		if !ok || len(fields) < 5 || len(fs) < 2 || fields[4] != "/" {
			continue
		}
		device, err := filepath.EvalSymlinks("/sys/dev/block/" + fields[2])
		if err == nil {
			root.State, root.Detail = "ready", "/dev/"+filepath.Base(device)+" · "+fs[0]
		} else {
			root.Detail = "根分区已挂载，块设备来源未确认"
		}
		break
	}
	return emmc, nvme, root
}

func bluetoothCheck() bootCheck {
	devices, _ := filepath.Glob("/sys/class/bluetooth/hci[0-9]*")
	if len(devices) == 0 {
		return bootCheck{"蓝牙", "missing", "未枚举到蓝牙 HCI"}
	}
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, filepath.Base(device))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	err := exec.CommandContext(ctx, "/usr/bin/systemctl", "is-active", "--quiet", "bluetooth.service").Run()
	label := summarize(names)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return bootCheck{"蓝牙", "timeout", label + " 已枚举，服务状态检查超时"}
	}
	if err != nil {
		return bootCheck{"蓝牙", "enumerated", label + " 已枚举，蓝牙服务未确认就绪"}
	}
	return bootCheck{"蓝牙", "ready", label + " 已枚举，蓝牙服务运行中"}
}

func (m *hardwareManager) systemChecks() []bootCheck {
	wired, wifi := interfaceChecks()
	emmc, nvme, root := storageChecks()
	zigbee := bootCheck{"Zigbee", "unknown", "无法确认协调器状态"}
	mapping := m.mapping(hardwareRequest{Operation: "mapping"})
	if mapping.Mapping != nil {
		zigbee.State = mapping.Mapping.State
		switch zigbee.State {
		case "connected":
			zigbee.Detail = mapping.Mapping.Device + " → " + coordinatorAlias
		case "unbound":
			zigbee.Detail = "尚未绑定协调器"
		case "missing":
			zigbee.Detail = "已绑定设备未连接"
		case "conflict":
			zigbee.Detail = "已绑定设备的身份存在冲突或不可用"
		default:
			zigbee.Detail = "固定设备名应用失败"
		}
	}
	return []bootCheck{wired, emmc, nvme, root, wifi, bluetoothCheck(), zigbee}
}
