package main

// 文本/事件/提问/点播/电话/车辆控制/区域/透传/RSA/驾驶员 类下行

import (
	"encoding/hex"
	"fmt"
	"strings"
)

func init() {
	handlers[0x8300] = (*Terminal).h8300
	handlers[0x8301] = (*Terminal).h8301
	handlers[0x8302] = (*Terminal).h8302
	handlers[0x8303] = (*Terminal).h8303
	handlers[0x8304] = (*Terminal).h8304
	handlers[0x8400] = (*Terminal).h8400
	handlers[0x8401] = (*Terminal).h8401
	handlers[0x8500] = (*Terminal).h8500
	for _, id := range []int{0x8600, 0x8602, 0x8604, 0x8606} {
		handlers[id] = (*Terminal).hSetArea
	}
	for _, id := range []int{0x8601, 0x8603, 0x8605, 0x8607} {
		handlers[id] = (*Terminal).hDelArea
	}
	handlers[0x8608] = (*Terminal).h8608
	handlers[0x8900] = (*Terminal).h8900
	handlers[0x8A00] = (*Terminal).h8A00
	handlers[0x8702] = (*Terminal).h8702
}

func flagNames(v int, names map[int]string) string {
	var s []string
	for i := 0; i < 8; i++ {
		if v&(1<<i) != 0 && names[i] != "" {
			s = append(s, names[i])
		}
	}
	return strings.Join(s, "/")
}

func (t *Terminal) h8300(m *Msg) {
	r := newReader(m.Body)
	sign := r.u8()
	typ := 0
	if m.Version >= V2019 {
		typ = r.u8()
	}
	text := ungbk(r.rest())
	names := map[int]string{0: "紧急", 2: "终端显示", 3: "TTS 播读", 4: "广告屏显示", 5: "CAN 故障码"}
	title := "文本信息"
	if f := flagNames(sign, names); f != "" {
		title += "（" + f + "）"
	}
	if typ > 0 {
		title += map[int]string{1: " 通知", 2: " 服务"}[typ]
	}
	t.addInbox("text", title, text, m.Serial, nil)
	t.ack(m, 0)
}

// h8301 事件设置：0 删除全部 1 更新 2 追加 3 修改 4 删除指定
func (t *Terminal) h8301(m *Msg) {
	r := newReader(m.Body)
	typ, n := r.u8(), r.u8()
	t.mu.Lock()
	if typ == 0 || typ == 1 {
		t.Events = map[int]string{}
	}
	var list []string
	for i := 0; i < n && r.err == nil; i++ {
		id := r.u8()
		s := r.lstr()
		if typ == 4 {
			delete(t.Events, id)
		} else {
			t.Events[id] = s
		}
		list = append(list, fmt.Sprintf("%d:%s", id, s))
	}
	t.markDirty()
	t.mu.Unlock()
	t.addInbox("event", "事件设置", strings.Join(list, "  "), 0, nil)
	t.ack(m, 0)
}

// h8302 提问下发：页面选择答案后回 0302
func (t *Terminal) h8302(m *Msg) {
	r := newReader(m.Body)
	sign := r.u8()
	q := r.lstr()
	var ans []Answer
	for r.left() > 0 && r.err == nil {
		id := r.u8()
		ans = append(ans, Answer{id, r.wstr()})
	}
	title := "提问"
	if f := flagNames(sign, map[int]string{0: "紧急", 3: "TTS 播读", 4: "广告屏显示"}); f != "" {
		title += "（" + f + "）"
	}
	t.addInbox("question", title, q, m.Serial, ans)
	t.ack(m, 0)
}

func (t *Terminal) answer(serial, answerID int) error {
	t.mu.Lock()
	found := false
	for i := range t.Inbox {
		if it := &t.Inbox[i]; it.Kind == "question" && it.Serial == serial {
			it.Answered, found = true, true
		}
	}
	t.markDirty()
	t.mu.Unlock()
	if !found {
		return fmt.Errorf("没有流水号 %d 的提问", serial)
	}
	_, _, err := t.send(0x0302, cat(u16(serial), []byte{byte(answerID)}), fmt.Sprintf("提问 %d 答案 %d", serial, answerID))
	return err
}

// h8303 信息点播菜单设置
func (t *Terminal) h8303(m *Msg) {
	r := newReader(m.Body)
	typ, n := r.u8(), r.u8()
	t.mu.Lock()
	if typ == 0 || typ == 1 {
		t.Menus = map[int]string{}
	}
	var list []string
	for i := 0; i < n && r.err == nil; i++ {
		id := r.u8()
		s := r.wstr()
		if typ == 3 {
			delete(t.Menus, id)
		} else {
			t.Menus[id] = s
		}
		list = append(list, fmt.Sprintf("%d:%s", id, s))
	}
	t.markDirty()
	t.mu.Unlock()
	t.addInbox("menu", "信息点播菜单", strings.Join(list, "  "), 0, nil)
	t.ack(m, 0)
}

func (t *Terminal) h8304(m *Msg) {
	r := newReader(m.Body)
	typ := r.u8()
	t.mu.Lock()
	name := t.Menus[typ]
	t.mu.Unlock()
	t.addInbox("info", fmt.Sprintf("信息服务 %d %s", typ, name), r.wstr(), 0, nil)
	t.ack(m, 0)
}

func (t *Terminal) h8400(m *Msg) {
	r := newReader(m.Body)
	typ := r.u8()
	phone := strings.TrimRight(r.str(min(20, r.left())), " ")
	t.addInbox("call", map[int]string{0: "电话回拨（通话）", 1: "电话回拨（监听）"}[typ], phone, 0, nil)
	t.ack(m, 0)
}

// h8401 电话本：0 删除全部 1 更新 2 追加 3 修改
func (t *Terminal) h8401(m *Msg) {
	r := newReader(m.Body)
	typ, n := r.u8(), r.u8()
	t.mu.Lock()
	if typ <= 1 {
		t.Phonebook = nil
	}
	var list []string
	for i := 0; i < n && r.err == nil; i++ {
		sign := r.u8()
		phone := r.lstr()
		name := r.lstr()
		e := map[string]any{"sign": sign, "phone": phone, "name": name}
		replaced := false
		if typ == 3 {
			for j, old := range t.Phonebook {
				if old["name"] == name {
					t.Phonebook[j], replaced = e, true
				}
			}
		}
		if !replaced {
			t.Phonebook = append(t.Phonebook, e)
		}
		list = append(list, fmt.Sprintf("%s %s(%s)", name, phone, map[int]string{1: "呼入", 2: "呼出", 3: "呼入/呼出"}[sign]))
	}
	t.markDirty()
	t.mu.Unlock()
	t.addInbox("phonebook", "设置电话本", strings.Join(list, "  "), 0, nil)
	t.ack(m, 0)
}

// vehCtlResult 8500 解析结果
type vehCtlResult struct {
	lock        bool
	legacy      bool  // 按网关旧格式（标志 BYTE + 类型 WORD + 参数 BYTE）解析
	unsupported []int // 不支持的控制类型 ID
}

// parse8500 车辆控制消息体。
//   - 2013：控制标志 BYTE，bit0 0=车门解锁 1=车门加锁
//   - 2019：控制类型数量 WORD + n×(控制类型ID WORD + 控制参数)；ID 0x0001 车门，参数 BYTE 0=车门锁闭 1=车门开启。
//     其它 ID 的参数长度标准未定义，无法继续解析，记为不支持
//   - 2019 且消息体恰为 4 字节：网关旧编码（标志 BYTE + 类型 WORD + 参数 BYTE，最后一字节 bit0=1 加锁）。
//     标准格式至少 5 字节（数量 + ID + 参数），4 字节不会与标准格式混淆
//   - 2019 且消息体恰为 3 字节：jt808-server（yezhihao）等网关的编码，缺少“控制类型数量”，
//     为 类型ID WORD + 参数 BYTE，参数 1=加锁 0=解锁（与该网关的接口说明一致）。
//     标准格式至少 5 字节，3 字节不会与标准格式混淆
func parse8500(version int, body []byte) (*vehCtlResult, error) {
	res := &vehCtlResult{}
	if version < V2019 {
		if len(body) < 1 {
			return nil, fmt.Errorf("消息体为空")
		}
		res.lock = body[0]&1 != 0
		return res, nil
	}
	if len(body) == 4 {
		res.legacy, res.lock = true, body[3]&1 != 0
		return res, nil
	}
	if len(body) == 3 {
		id := int(body[0])<<8 | int(body[1])
		if id != 0x0001 {
			res.unsupported = append(res.unsupported, id)
			return res, nil
		}
		res.legacy, res.lock = true, body[2]&1 != 0
		return res, nil
	}
	r := newReader(body)
	n := r.u16()
	if r.err != nil || n == 0 {
		return nil, fmt.Errorf("控制类型数量无效")
	}
	door := false
	for i := 0; i < n && r.err == nil; i++ {
		id := r.u16()
		if r.err != nil {
			break
		}
		if id != 0x0001 {
			res.unsupported = append(res.unsupported, id)
			return res, nil
		}
		res.lock, door = r.u8() == 0, true
	}
	if r.err != nil {
		return nil, r.err
	}
	if !door {
		return nil, fmt.Errorf("没有控制类型")
	}
	return res, nil
}

// h8500 车辆控制 -> 0500（应答流水号 + 位置信息，状态位 bit12 反映车门加锁结果）；不支持的控制类型回 0001 结果 3。
// 配置 reply8500=ack0500 时先回 0001 通用应答（结果 0）再回 0500（部分平台要求）；默认只回 0500

func (t *Terminal) h8500(m *Msg) {
	res, err := parse8500(m.Version, m.Body)
	if err != nil {
		t.errf("8500 解析失败: %v", err)
		t.ack(m, 2)
		return
	}
	if len(res.unsupported) > 0 {
		t.addInbox("control", "车辆控制", fmt.Sprintf("不支持的控制类型 0x%04X", res.unsupported[0]), 0, nil)
		t.ack(m, 3)
		return
	}
	lock := res.lock
	t.mu.Lock()
	if lock {
		t.StatusOn |= 1 << 12
	} else {
		t.StatusOn &^= 1 << 12
	}
	t.markDirty()
	body := cat(u16(m.Serial), t.locBody(nil))
	ackFirst := t.Cfg.Reply8500 == "ack0500"
	t.mu.Unlock()
	if ackFirst {
		t.ack(m, 0)
	}
	note := map[bool]string{true: "车门加锁", false: "车门解锁"}[lock]
	if res.legacy {
		note += "（网关旧格式）"
	}
	t.addInbox("control", "车辆控制", note, 0, nil)
	t.send(0x0500, body, map[bool]string{true: "车门已加锁", false: "车门已解锁"}[lock])
}

func (t *Terminal) hSetArea(m *Msg) {
	mode, list, err := parseAreas(m.ID, m.Version, m.Body)
	if err != nil {
		t.errf("%04X 解析失败: %v", m.ID, err)
		t.ack(m, 2)
		return
	}
	kind := list[0].Kind
	t.mu.Lock()
	t.applyAreas(kind, mode, list)
	t.mu.Unlock()
	var s []string
	for _, a := range list {
		s = append(s, fmt.Sprintf("%d「%s」%d 点 属性=%04X", a.ID, a.Name, len(a.Points), a.Attr))
	}
	t.addInbox("area", "设置"+areaKindName[kind], strings.Join(s, "  "), 0, nil)
	t.ack(m, 0)
}

func (t *Terminal) hDelArea(m *Msg) {
	kind := map[int]int{0x8601: 1, 0x8603: 2, 0x8605: 3, 0x8607: 4}[m.ID]
	r := newReader(m.Body)
	n := r.u8()
	var ids []int
	for i := 0; i < n && r.err == nil; i++ {
		ids = append(ids, r.u32())
	}
	t.mu.Lock()
	del := t.deleteAreas(kind, ids)
	t.mu.Unlock()
	t.addInbox("area", "删除"+areaKindName[kind], fmt.Sprintf("ID %v，删除 %d 个", ids, del), 0, nil)
	t.ack(m, 0)
}

func (t *Terminal) h8608(m *Msg) {
	r := newReader(m.Body)
	kind, n := r.u8(), r.u32()
	var ids []int
	for i := 0; i < n && r.err == nil; i++ {
		ids = append(ids, r.u32())
	}
	t.mu.Lock()
	body := t.areaQuery(kind, ids)
	t.mu.Unlock()
	t.send(0x0608, body, areaKindName[kind])
}

func (t *Terminal) h8900(m *Msg) {
	t.mu.Lock()
	yue := t.Cfg.yue()
	t.mu.Unlock()
	if yue && t.gdH8900(m) {
		return
	}
	r := newReader(m.Body)
	typ := r.u8()
	t.addInbox("passthrough", fmt.Sprintf("下行透传 类型=0x%02X", typ), fmt.Sprintf("%X", r.rest()), 0, nil)
	t.ack(m, 0)
}

// h8A00 平台 RSA 公钥：保存（上行加密用）后回复终端公钥 0A00（e(4) + n(128)）
func (t *Terminal) h8A00(m *Msg) {
	r := newReader(m.Body)
	e := r.u32()
	n := r.take(128)
	if r.err != nil {
		t.ack(m, 2)
		return
	}
	t.mu.Lock()
	t.PlatRSA = PlatKey{E: e, N: hex.EncodeToString(n)}
	t.markDirty()
	key := t.termRSA()
	t.mu.Unlock()
	t.addInbox("rsa", "平台 RSA 公钥", fmt.Sprintf("e=%d n=%X…（已保存，上行RSA加密开启时使用）", e, n[:16]), 0, nil)
	if key == nil {
		t.ack(m, 1)
		return
	}
	t.send(0x0A00, cat(u32(key.E), key.N.FillBytes(make([]byte, rsaBlock))), "终端 RSA 公钥 1024 位")
}

// h8702 上报驾驶员身份信息请求 -> 0702
func (t *Terminal) h8702(m *Msg) {
	t.send(0x0702, t.bDriver(), "驾驶员身份")
}
