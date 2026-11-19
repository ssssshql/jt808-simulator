package main

// 命令行无界面运行场景：jt808sim-cli -scenario file.yaml [-host ... -port ... -phone ...] [-timeout 10m]
// 用于 CI / 自动化回归：运行结束打印摘要；全部步骤与断言通过时退出码 0，否则 1（场景文件无效为 2）。

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// scenarioExit 场景运行结果对应的退出码
func scenarioExit(v ScenRunView) int {
	if v.Status == "done" && v.Failures == 0 {
		return 0
	}
	return 1
}

// formatScenarioSummary 运行摘要（中文，便于在 CI 日志中查看）
func formatScenarioSummary(v ScenRunView) string {
	var b strings.Builder
	status := map[string]string{"done": "完成", "stopped": "已停止", "error": "出错", "running": "运行中"}[v.Status]
	fmt.Fprintf(&b, "场景「%s」%s，用时 %.1fs，步骤 %d/%d，失败 %d，断言 %d（失败 %d）\n",
		v.Name, status, v.Elapsed, min(v.Step, v.Total), v.Total, v.Failures, v.Asserts, v.AssertFails)
	if v.Error != "" {
		fmt.Fprintf(&b, "  错误：%s\n", v.Error)
	}
	for _, l := range v.Log {
		if l.OK {
			continue
		}
		step := "准备"
		if l.Step >= 0 {
			step = fmt.Sprintf("第 %d 步", l.Step+1)
		}
		fmt.Fprintf(&b, "  ✗ [%6.1fs] %s %s：%s\n", l.T, step, l.Text, l.Error)
	}
	if scenarioExit(v) == 0 {
		b.WriteString("结果：通过\n")
	} else {
		b.WriteString("结果：失败\n")
	}
	return b.String()
}

// runScenarioCLI 读取场景文件并在单个终端上运行，返回退出码
func runScenarioCLI(path string, flagCfg Config, timeout time.Duration) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取场景文件失败:", err)
		return 2
	}
	sc, err := ParseScenario(string(data))
	if err != nil {
		fmt.Fprintln(os.Stderr, "场景无效:", err)
		return 2
	}
	if t := strings.TrimSpace(sc.Terminal); t != "" && t != "1" {
		fmt.Fprintf(os.Stderr, "命令行模式只有一个终端，忽略场景中的 terminal: %q\n", sc.Terminal)
	}
	sc.Terminal = ""
	if flagCfg.Phone == "" {
		flagCfg.Phone = strings.TrimSpace(fmt.Sprint(sc.Config["phone"]))
		if _, ok := sc.Config["phone"]; !ok {
			flagCfg.Phone = ""
		}
	}
	if flagCfg.Phone == "" {
		fmt.Fprintln(os.Stderr, "缺少终端手机号：请用 -phone 指定，或在场景 config.phone 中填写")
		return 2
	}
	m := &Manager{next: 2, noSave: true} // 只在内存中管理这一个终端，不写 terminals.json
	t := m.spawn("1", flagCfg)
	m.mu.Lock()
	m.terms, m.def = []*Terminal{t}, "1"
	m.mu.Unlock()
	defer func() {
		t.Stop("场景结束")
		close(t.quit)
		t.mu.Lock()
		t.save()
		t.mu.Unlock()
	}()
	if _, err := scenarios.Run(m, sc, "1"); err != nil {
		fmt.Fprintln(os.Stderr, "启动场景失败:", err)
		return 2
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	var deadline <-chan time.Time
	if timeout > 0 {
		deadline = time.After(timeout)
	}
	done := scenarios.done("1")
	select {
	case <-done:
	case <-sig:
		fmt.Fprintln(os.Stderr, "收到中断信号，停止场景")
		scenarios.Stop("1")
	case <-deadline:
		fmt.Fprintf(os.Stderr, "超过 -timeout %s，停止场景\n", timeout)
		scenarios.Stop("1")
	}
	views := scenarios.Views(m)
	if len(views) == 0 {
		return 1
	}
	fmt.Print(formatScenarioSummary(views[0]))
	return scenarioExit(views[0])
}
