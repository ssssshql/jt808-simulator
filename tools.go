package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"jt808sim/internal/codec"
)

type toolField struct {
	Name string `json:"name"`
	Hex  string `json:"hex"`
	Val  string `json:"val"`
	Desc string `json:"desc,omitempty"`
}

func registerToolAPI(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/tool/parse", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		rawHex := cleanHexInput(pstr(p, "hex", ""))
		if rawHex == "" {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "请输入十六进制报文"})
			return
		}
		b, err := hex.DecodeString(rawHex)
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "十六进制格式错误: " + err.Error()})
			return
		}
		if len(b) >= 24 && b[0] == 0x30 && b[1] == 0x31 && b[2] == 0x63 && b[3] == 0x64 {
			writeJSON(w, 200, parse1078Tool(b))
			return
		}
		if len(b) >= 2 && b[0] == 0x7e && b[len(b)-1] == 0x7e {
			b = b[1 : len(b)-1]
		}
		m := codec.Parse(b)
		if m == nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "报文格式无效（转义非法或消息头被截断）"})
			return
		}
		fields := []toolField{
			{Name: "消息 ID", Hex: fmt.Sprintf("%04X", m.ID), Val: msgName(m.ID)},
			{Name: "协议版本", Hex: fmt.Sprintf("%d", m.Version), Val: fmt.Sprintf("JT/T 808-%d", m.Version)},
			{Name: "终端手机号", Hex: m.Phone, Val: m.Phone},
			{Name: "消息流水号", Hex: fmt.Sprintf("%04X", m.Serial), Val: fmt.Sprintf("%d", m.Serial)},
			{Name: "校验码", Hex: "-", Val: map[bool]string{true: "校验通过", false: "校验失败"}[m.OK]},
		}
		if m.Total > 0 {
			fields = append(fields, toolField{Name: "分包信息", Hex: fmt.Sprintf("%04X/%04X", m.Total, m.Index), Val: fmt.Sprintf("第 %d/%d 包", m.Index, m.Total)})
		}
		if m.RSA {
			fields = append(fields, toolField{Name: "加密标志", Hex: "bit10=1", Val: "RSA 加密"})
		}
		fields = append(fields, toolField{Name: "消息体长度", Hex: fmt.Sprintf("%d", len(m.Body)), Val: fmt.Sprintf("%d 字节", len(m.Body))})
		if !m.RSA && len(m.Body) > 0 {
			fields = append(fields, decodeToolBody(m.ID, m.Version, m.Body)...)
		}
		writeJSON(w, 200, map[string]any{
			"ok":      true,
			"proto":   fmt.Sprintf("JT/T 808-%d", m.Version),
			"msgId":   fmt.Sprintf("%04X", m.ID),
			"msgName": msgName(m.ID),
			"crcOK":   m.OK,
			"bodyHex": strings.ToUpper(hex.EncodeToString(m.Body)),
			"fields":  fields,
		})
	})

	mux.HandleFunc("POST /api/tool/build", func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeJSON(w, 400, map[string]any{"ok": false, "error": "JSON 无效"})
			return
		}
		id64, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(pstr(p, "msgId", "0200")), "0X"), 16, 32)
		if err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": "消息 ID 格式错误"})
			return
		}
		bodyHex := cleanHexInput(pstr(p, "bodyHex", ""))
		var body []byte
		if bodyHex != "" {
			if body, err = hex.DecodeString(bodyHex); err != nil {
				writeJSON(w, 200, map[string]any{"ok": false, "error": "消息体 hex 错误: " + err.Error()})
				return
			}
		}
		ver := pint(p, "version", 2019)
		if ver != 2011 && ver != 2013 && ver != 2019 {
			ver = 2019
		}
		phone := pstr(p, "phone", "13800000001")
		seq := pint(p, "seq", 1)
		frame := codec.Frame(int(id64), phone, seq, body, ver, 0, 0)
		writeJSON(w, 200, map[string]any{
			"ok":  true,
			"hex": strings.ToUpper(hex.EncodeToString(frame)),
			"len": len(frame),
		})
	})
}

func cleanHexInput(s string) string {
	s = strings.ReplaceAll(s, "0x", "")
	s = strings.ReplaceAll(s, "0X", "")
	var b strings.Builder
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parse1078Tool(b []byte) map[string]any {
	pt, dt, tot, hl, simLen, ok := parse1078Packet(b, 10)
	if !ok {
		return map[string]any{"ok": false, "error": "JT/T 1078 包头解析失败"}
	}
	dtName := map[int]string{0: "视频I帧", 1: "视频P帧", 2: "视频B帧", 3: "音频帧", 4: "透传"}[dt]
	ch := b[8+simLen]
	seq := binary.BigEndian.Uint16(b[6:8])
	sim := strings.TrimLeft(hex.EncodeToString(b[8:8+simLen]), "0")
	fields := []toolField{
		{Name: "帧头标识", Hex: "30316364", Val: "JT/T 1078 RTP"},
		{Name: "包序号", Hex: fmt.Sprintf("%04X", seq), Val: fmt.Sprintf("%d", seq)},
		{Name: "SIM 卡号", Hex: strings.ToUpper(hex.EncodeToString(b[8 : 8+simLen])), Val: fmt.Sprintf("%s (%dB)", sim, simLen)},
		{Name: "逻辑通道", Hex: fmt.Sprintf("%02X", ch), Val: fmt.Sprintf("通道 %d", ch)},
		{Name: "数据类型", Hex: fmt.Sprintf("%d", dt), Val: dtName},
		{Name: "负载类型 PT", Hex: fmt.Sprintf("%d", pt), Val: fmt.Sprintf("PT=%d", pt)},
		{Name: "包总长 / 体长", Hex: fmt.Sprintf("%d/%d", tot, tot-hl), Val: fmt.Sprintf("%d / %d 字节", tot, tot-hl)},
	}
	return map[string]any{"ok": true, "proto": "JT/T 1078", "msgId": "1078", "msgName": dtName, "crcOK": true, "fields": fields}
}

func decodeToolBody(id, ver int, b []byte) []toolField {
	var out []toolField
	rd := codec.NewReader(b)
	switch id {
	case 0x0001, 0x8001:
		if len(b) >= 5 {
			seq, rid, res := rd.U16(), rd.U16(), rd.U8()
			out = append(out,
				toolField{Name: "应答流水号", Hex: fmt.Sprintf("%04X", seq), Val: fmt.Sprintf("%d", seq)},
				toolField{Name: "应答消息 ID", Hex: fmt.Sprintf("%04X", rid), Val: msgName(rid)},
				toolField{Name: "结果", Hex: fmt.Sprintf("%02X", res), Val: fmt.Sprintf("%d", res)},
			)
		}
	case 0x0100:
		if len(b) >= 25 {
			prov, city := rd.U16(), rd.U16()
			makerLen, modelLen, devLen := 5, 20, 7
			if ver >= 2019 {
				makerLen, modelLen, devLen = 11, 30, 30
			} else if ver == 2011 {
				makerLen, modelLen, devLen = 5, 8, 7
			}
			maker := rd.Str(makerLen)
			model := rd.Str(modelLen)
			dev := rd.Str(devLen)
			color := rd.U8()
			plate := codec.UnGBK(rd.Rest())
			out = append(out,
				toolField{Name: "省域 / 市县 ID", Hex: fmt.Sprintf("%04X/%04X", prov, city), Val: fmt.Sprintf("%d / %04d", prov, city)},
				toolField{Name: "制造商 ID", Hex: maker, Val: maker},
				toolField{Name: "终端型号", Hex: model, Val: model},
				toolField{Name: "终端 ID", Hex: dev, Val: dev},
				toolField{Name: "车牌颜色 / 车牌", Hex: fmt.Sprintf("%02X", color), Val: fmt.Sprintf("颜色 %d · %s", color, plate)},
			)
		}
	case 0x0200:
		if len(b) >= 28 {
			al, st, lat, lng := rd.U32(), rd.U32(), rd.U32(), rd.U32()
			alt, spd, dir := rd.U16(), rd.U16(), rd.U16()
			tm := rd.BCD(6)
			out = append(out,
				toolField{Name: "报警标志", Hex: fmt.Sprintf("%08X", al), Val: fmt.Sprintf("0x%08X", al)},
				toolField{Name: "状态位", Hex: fmt.Sprintf("%08X", st), Val: fmt.Sprintf("ACC=%d 定位=%d", st&1, (st>>1)&1)},
				toolField{Name: "经度 / 纬度", Hex: fmt.Sprintf("%08X/%08X", lng, lat), Val: fmt.Sprintf("%.6f, %.6f", float64(lng)/1e6, float64(lat)/1e6)},
				toolField{Name: "高程 / 速度 / 方向", Hex: fmt.Sprintf("%04X/%04X/%04X", alt, spd, dir), Val: fmt.Sprintf("%dm · %.1f km/h · %d°", alt, float64(spd)/10, dir)},
				toolField{Name: "时间 (GMT+8)", Hex: tm, Val: tm},
			)
			for rd.Left() >= 2 {
				eid := int(rd.U8())
				elen := int(rd.U8())
				if rd.Left() < elen {
					break
				}
				eb := rd.Take(elen)
				val := fmt.Sprintf("%d 字节", elen)
				switch {
				case eid == 0x01 && len(eb) == 4:
					val = fmt.Sprintf("里程 %.1f km", float64(binary.BigEndian.Uint32(eb))/10)
				case eid == 0x02 && len(eb) == 2:
					val = fmt.Sprintf("油量 %.1f L", float64(binary.BigEndian.Uint16(eb))/10)
				case eid == 0x03 && len(eb) == 2:
					val = fmt.Sprintf("记录速度 %.1f km/h", float64(binary.BigEndian.Uint16(eb))/10)
				case eid == 0x30 && len(eb) == 1:
					val = fmt.Sprintf("信号强度 %d", eb[0])
				case eid == 0x31 && len(eb) == 1:
					val = fmt.Sprintf("卫星数 %d", eb[0])
				case eid >= 0x64 && eid <= 0x67 && len(eb) >= 6:
					val = fmt.Sprintf("主动安全报警 ID=%d 类型=0x%02X 级别=%d", binary.BigEndian.Uint32(eb[0:4]), eb[5], eb[4])
				}
				out = append(out, toolField{Name: fmt.Sprintf("附加 0x%02X", eid), Hex: strings.ToUpper(hex.EncodeToString(eb)), Val: val})
			}
		}
	case 0x8100:
		if len(b) >= 3 {
			seq, res := rd.U16(), rd.U8()
			tok := codec.UnGBK(rd.Rest())
			out = append(out,
				toolField{Name: "应答流水号", Hex: fmt.Sprintf("%04X", seq), Val: fmt.Sprintf("%d", seq)},
				toolField{Name: "结果", Hex: fmt.Sprintf("%02X", res), Val: fmt.Sprintf("%d", res)},
				toolField{Name: "鉴权码", Hex: tok, Val: tok},
			)
		}
	case 0x8103, 0x0104:
		if id == 0x0104 && len(b) >= 3 {
			seq := rd.U16()
			out = append(out, toolField{Name: "应答流水号", Hex: fmt.Sprintf("%04X", seq), Val: fmt.Sprintf("%d", seq)})
		}
		if rd.Left() >= 1 {
			cnt := rd.U8()
			out = append(out, toolField{Name: "参数个数", Hex: fmt.Sprintf("%02X", cnt), Val: fmt.Sprintf("%d 项", cnt)})
			for i := 0; i < cnt && rd.Left() >= 5; i++ {
				pid := rd.U32()
				plen := rd.U8()
				if rd.Left() < plen {
					break
				}
				pb := rd.Take(plen)
				pname := fmt.Sprintf("参数 0x%04X", pid)
				if d := pdef(pid); d != nil {
					pname = fmt.Sprintf("0x%04X %s", pid, d.Name)
				}
				out = append(out, toolField{Name: pname, Hex: strings.ToUpper(hex.EncodeToString(pb)), Val: decodeParam(pid, pb)})
			}
		}
	case 0x8300:
		if len(b) >= 1 {
			fl := rd.U8()
			txt := codec.UnGBK(rd.Rest())
			out = append(out,
				toolField{Name: "标志", Hex: fmt.Sprintf("%02X", fl), Val: fmt.Sprintf("0x%02X", fl)},
				toolField{Name: "文本内容", Hex: "-", Val: txt},
			)
		}
	case 0x0900, 0x8900:
		if len(b) >= 1 {
			tp := rd.U8()
			rest := rd.Rest()
			out = append(out,
				toolField{Name: "透传类型", Hex: fmt.Sprintf("%02X", tp), Val: fmt.Sprintf("0x%02X", tp)},
				toolField{Name: "透传内容", Hex: strings.ToUpper(hex.EncodeToString(rest)), Val: codec.UnGBK(rest)},
			)
		}
	case 0x9101:
		if len(b) >= 7 {
			ip := rd.LStr()
			tcp, udp, ch, dt, st := rd.U16(), rd.U16(), rd.U8(), rd.U8(), rd.U8()
			out = append(out,
				toolField{Name: "媒体服务器", Hex: "-", Val: fmt.Sprintf("%s (TCP:%d UDP:%d)", ip, tcp, udp)},
				toolField{Name: "通道 / 类型 / 码流", Hex: fmt.Sprintf("%02X/%02X/%02X", ch, dt, st), Val: fmt.Sprintf("CH%d · 数据类型=%d · 码流=%d", ch, dt, st)},
			)
		}
	case 0x9208:
		if len(b) >= 20 {
			ip := rd.LStr()
			tcp, udp := rd.U16(), rd.U16()
			out = append(out, toolField{Name: "附件服务器", Hex: "-", Val: fmt.Sprintf("%s (TCP:%d UDP:%d)", ip, tcp, udp)})
		}
	case 0x1210:
		if len(b) >= 20 {
			dev := rd.Str(7)
			if ver >= 2019 && len(b) >= 75 {
				dev = rd.Str(30)
			}
			out = append(out, toolField{Name: "终端 ID", Hex: dev, Val: dev})
		}
	case 0x8600, 0x8602, 0x8604, 0x8606:
		if len(b) >= 2 {
			op := rd.U8()
			cnt := rd.U8()
			out = append(out,
				toolField{Name: "操作属性", Hex: fmt.Sprintf("%02X", op), Val: fmt.Sprintf("%d", op)},
				toolField{Name: "区域/路线数", Hex: fmt.Sprintf("%02X", cnt), Val: fmt.Sprintf("%d", cnt)},
			)
		}
	}
	return out
}
