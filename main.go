package main

// JT808 终端模拟器：命令行 / Web 界面（-web）。用法见 README.md，或运行后输入 help

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const help = `
连接
  login / connect           上线（无鉴权码先注册，断线按退避自动重连）
  close / disconnect        下线          logout   注销(0003)并清除鉴权码
位置与运动
  loc [经度 纬度]            设定位置并立即汇报      report  立即汇报 0200
  drive [速度] / circle [速度] / route [速度] / stop   运动模式
  mileage <km> / fuel <L>
状态与报警
  status <位> on|off        状态位（0 ACC，12 车门加锁，13 门1 ...）
  alarm <位> on|off         报警位（0 紧急 1 超速 2 疲劳 29 碰撞侧翻 ...）
  video <位> on|off         视频报警位          clear   清除全部报警
  adas [kind type level files]   主动安全报警，kind=adas/dsm/tpms/bsd
上行报文
  send <消息ID> [k=v ...]   如 send 0702 status=1 name=李四；send 0704 count=5 type=1
  0108 0301 0303 0702 0704 0705 0800 0801 0900 0901 0701 1005 [k=v ...]   同 send
  raw <消息ID> <hex 消息体>
  burst <消息ID> [次数]      连发，测试网关限流
下行应答
  reply auto|fail|unsupported|none     delay <秒>
  answer <提问流水号> <答案ID>
  attach on|off|host <ip>   9208 后是否自动上传附件 / 覆盖附件服务器 IP
其它
  param <ID hex> [值]       查看/设置终端参数        params  列出全部参数
  photo [通道]              拍照并上传               stream stop <通道>
  info / sleep <秒> / verbose on|off / help / quit
`

func parseKV(args []string) map[string]any {
	kv := map[string]any{}
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			kv[strings.TrimSpace(k)] = v
		}
	}
	// 兼容旧参数名
	for old, cur := range map[string]string{"event_id": "eventId", "card_status": "cardStatus", "license_no": "licenseNo"} {
		if v, ok := kv[old]; ok {
			kv[cur] = v
		}
	}
	return kv
}

func onOff(args []string, i int) int {
	if i < len(args) && args[i] == "off" {
		return 0
	}
	return 1
}

func arg(args []string, i int, def string) string {
	if i < len(args) {
		return args[i]
	}
	return def
}

// run 执行一行命令；返回 false 表示退出
func run(t *Terminal, line string) (bool, error) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return true, nil
	}
	cmd, args := strings.ToLower(parts[0]), parts[1:]
	A := func(typ string, p map[string]any) (bool, error) { return true, t.Do(typ, p) }
	switch cmd {
	case "quit", "exit", "q":
		return false, nil
	case "help", "h", "?":
		t.out(strings.Trim(help, "\n"))
	case "login", "connect":
		return A("connect", nil)
	case "close", "disconnect":
		return A("disconnect", nil)
	case "logout":
		return A("logout", nil)
	case "loc":
		if len(args) >= 2 {
			return A("setPosition", map[string]any{"lng": args[0], "lat": args[1]})
		}
		return A("report", nil)
	case "report":
		return A("report", nil)
	case "drive", "circle", "route", "stop":
		p := map[string]any{"mode": cmd}
		if len(args) > 0 {
			p["speed"] = args[0]
		}
		return A("setMotion", p)
	case "track": // 兼容旧命令：track <秒>|off
		if onOff(args, 0) == 0 {
			return A("setMotion", map[string]any{"mode": "stop"})
		}
		if len(args) > 0 {
			t.Do("setParam", map[string]any{"id": "0029", "value": args[0]})
		}
		return A("setMotion", map[string]any{"mode": "drive"})
	case "mileage", "fuel":
		return A("setVehicle", map[string]any{cmd: arg(args, 0, "")})
	case "status", "alarm", "video":
		if len(args) == 0 {
			if cmd == "status" {
				return run(t, "info")
			}
			return true, fmt.Errorf("用法: %s <位> on|off", cmd)
		}
		typ := map[string]string{"status": "setStatus", "alarm": "setAlarm", "video": "setVideoAlarm"}[cmd]
		return A(typ, map[string]any{"bit": args[0], "on": onOff(args, 1)})
	case "clear":
		return A("clearAlarms", nil)
	case "adas":
		kind := "adas"
		if len(args) > 0 && adasKinds[args[0]] != 0 {
			kind, args = args[0], args[1:]
		}
		return A("adas", map[string]any{"kind": kind, "type": arg(args, 0, "1"), "level": arg(args, 1, "1"), "files": arg(args, 2, "3")})
	case "send":
		if len(args) == 0 {
			return true, fmt.Errorf("用法: send <消息ID> [k=v ...]")
		}
		return A("send", map[string]any{"msgId": args[0], "params": parseKV(args[1:])})
	case "0108", "0301", "0303", "0702", "0704", "0705", "0800", "0801", "0900", "0901", "0701", "1005", "0002", "0004":
		return A("send", map[string]any{"msgId": cmd, "params": parseKV(args)})
	case "notify":
		for _, s := range []string{"0108", "0301", "0303", "0702 status=1", "0800", "0801", "0900", "0900 size=3000", "0901", "0702 status=2"} {
			if _, err := run(t, s); err != nil {
				return true, err
			}
			time.Sleep(1500 * time.Millisecond) // 大于网关限流窗口(1s)
		}
	case "burst":
		if len(args) == 0 {
			return true, fmt.Errorf("用法: burst <消息ID> [次数]")
		}
		n, _ := strconv.Atoi(arg(args, 1, "5"))
		for i := 0; i < n; i++ {
			if err := t.Do("send", map[string]any{"msgId": args[0], "params": parseKV(args[min(2, len(args)):])}); err != nil {
				return true, err
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.logf("burst %s x%d：Web 端应只收到 1 条（1s 限流窗口）", args[0], n)
	case "raw":
		return A("raw", map[string]any{"line": strings.Join(args, " ")})
	case "reply":
		mode := arg(args, 0, "auto")
		if mode == "ok" {
			mode = "auto"
		}
		return A("setReply", map[string]any{"mode": mode})
	case "delay":
		return A("setReply", map[string]any{"delay": arg(args, 0, "0")})
	case "answer":
		return A("answer", map[string]any{"serial": arg(args, 0, ""), "answerId": arg(args, 1, "")})
	case "attach":
		if arg(args, 0, "") == "host" {
			return A("setConfig", map[string]any{"attachHost": arg(args, 1, "")})
		}
		return A("setConfig", map[string]any{"autoAttach": onOff(args, 0)})
	case "param":
		if len(args) >= 2 {
			return A("setParam", map[string]any{"id": args[0], "value": strings.Join(args[1:], " ")})
		}
		id, err := strconv.ParseInt(arg(args, 0, ""), 16, 32)
		if err != nil {
			return true, fmt.Errorf("用法: param <ID hex> [值]")
		}
		t.mu.Lock()
		v, ok := t.Params[int(id)]
		t.mu.Unlock()
		if !ok {
			return true, fmt.Errorf("参数 %04X 未设置", id)
		}
		t.logf("%04X = %s", id, v)
	case "params":
		t.mu.Lock()
		views := t.paramViews()
		t.mu.Unlock()
		var b strings.Builder
		for _, v := range views {
			fmt.Fprintf(&b, "%s %-6s %-28s %s\n", v.ID, v.Type, v.Name, v.Value)
		}
		t.out(strings.TrimRight(b.String(), "\n"))
	case "photo":
		return A("photo", map[string]any{"channel": arg(args, 0, "1")})
	case "stream":
		return A("stopStream", map[string]any{"channel": arg(args, 1, arg(args, 0, "0"))})
	case "info":
		s := t.State()
		c := s["conn"].(ConnView)
		m := s["motion"].(map[string]any)
		t.logf("phone=%s 版本=%s 状态=%s 鉴权码=%s 位置=%s 运动=%v 汇报=%v(%vs) 计数=%v", c.Phone, c.Version, c.Status, c.Token,
			t.locSummary(), m["mode"], m["reportRule"], m["reportInterval"], s["counters"])
	case "verbose":
		Quiet = onOff(args, 0) == 0
	case "sleep":
		sec, _ := strconv.ParseFloat(arg(args, 0, "1"), 64)
		time.Sleep(time.Duration(sec * float64(time.Second)))
	default:
		t.errf("未知命令: %s（help 查看帮助）", cmd)
	}
	return true, nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ";") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// desktopMain 由 desktop.go（构建标签 desktop）设置：不带参数启动时打开桌面窗口
var desktopMain func()

func main() {
	if desktopMain != nil && len(os.Args) == 1 {
		desktopMain()
		return
	}
	host := flag.String("host", "", "网关地址（默认 127.0.0.1 或上次保存值）")
	port := flag.Int("port", 0, "网关 TCP 端口（默认 7611 或上次保存值）")
	phone := flag.String("phone", "", "终端手机号 = VEHICLE.DEVICE_ID")
	ver := flag.String("ver", "", "协议版本 2013|2019（默认 2019）")
	plate := flag.String("plate", "", "车牌（默认 粤A12345）")
	deviceID := flag.String("device-id", "", "终端ID（默认手机号后 7 位）")
	token := flag.String("token", "", "已知鉴权码（VEHICLE.AUTHORIZE_CODE），跳过注册")
	web := flag.String("web", "", "以 Web 界面启动，值为监听地址，例: -web 127.0.0.1:8090（-phone 可在页面填写）")
	showVer := flag.Bool("version", false, "打印版本信息后退出")
	var cmds multiFlag
	flag.Var(&cmds, "c", "启动后依次执行的命令，可多次指定；例: -c login -c notify -c quit")
	flag.Parse()
	if *showVer {
		fmt.Println(versionString())
		return
	}
	if *ver != "" && *ver != "2013" && *ver != "2019" {
		fmt.Fprintln(os.Stderr, "-ver 只能是 2013 或 2019")
		os.Exit(2)
	}
	if *web == "" && *phone == "" {
		fmt.Fprintln(os.Stderr, "缺少 -phone。示例: jt808sim -host 127.0.0.1 -port 7611 -phone 13800000001（或 -web 127.0.0.1:8090）")
		flag.PrintDefaults()
		os.Exit(2)
	}
	flagCfg := Config{Host: *host, Port: *port, Phone: *phone, Version: *ver, Plate: *plate, DeviceID: *deviceID, Token: *token}
	if *web != "" { // Web 模式：多终端（state/terminals.json），-c 命令作用于默认终端
		m := NewManager(flagCfg)
		logDisk.Start() // 报文日志落盘（state/logs，可在页面关闭）
		stopSave := make(chan struct{})
		defer close(stopSave)
		go m.saveLoopStop(stopSave)
		go func() { // systemd stop / Ctrl+C：先保存全部终端状态
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
			<-sig
			m.SaveAll()
			os.Exit(0)
		}()
		t := m.Get("")
		for _, line := range cmds {
			if _, err := run(t, line); err != nil {
				t.errf("%v", err)
			}
		}
		if err := serveWeb(*web, m); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	t := NewTerminal(flagCfg)
	go t.motionLoop()
	go t.saveLoop()
	defer func() {
		close(t.quit)
		t.mu.Lock()
		t.save()
		t.mu.Unlock()
	}()
	exec := func(line string) bool {
		ok, err := run(t, line)
		if err != nil {
			t.errf("%v", err)
		}
		return ok
	}
	for _, line := range cmds {
		fmt.Println("> " + line)
		if !exec(line) {
			t.Stop("退出")
			return
		}
	}
	fmt.Printf("JT808 模拟器  phone=%s  版本=%s  输入 help 查看命令\n", t.Cfg.Phone, t.Cfg.Version)
	sc := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("jt808> ")
		if !sc.Scan() || !exec(sc.Text()) {
			break
		}
	}
	t.Stop("退出")
}
