package main

// 下行消息体解析的模糊测试：平台下发任意字节都不得 panic。
// 帧层（转义、切帧、分包重组）的模糊测试在 internal/codec。
// 运行：go test . -run '^$' -fuzz Fuzz8103 -fuzztime 30s

import (
	"testing"
)

// 8103 设置终端参数：参数项 TLV 解析 + 按参数类型解码 + 粤标结构化参数合并
func Fuzz8103(f *testing.F) {
	f.Add(cat([]byte{2}, u32(0x0001), []byte{4}, u32(60), u32(0x0083), []byte{8}, gbk("粤A12345")))
	f.Add(cat([]byte{1}, u32(0xF364), []byte{3}, []byte{1, 2, 3}))
	f.Add([]byte{255, 0, 0})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, body []byte) {
		items, err := parse8103(body)
		if err != nil {
			return
		}
		for _, it := range items {
			v := decodeParam(it.id, it.raw)
			validateType(it.id, v)
			validateParam(it.id, v)
			if d := gdParamIndex[it.id]; d != nil && d.Schema != nil {
				gdMerge(it.id, "", it.raw)
			}
		}
	})
}

// 8600~8606 设置区域 / 路线：两种编码（标准 / 网关宽松）都要能安全失败；解析成功的区域可用于判定
func FuzzAreas(f *testing.F) {
	circle := cat([]byte{0, 1}, u32(1), u16(0), u32(23120000), u32(113260000), u32(500))
	f.Add(uint16(0x8600), true, circle)
	f.Add(uint16(0x8602), false, cat([]byte{1, 1}, u32(2), u16(1), u32(23130000), u32(113250000), u32(23110000), u32(113270000), bcdNow(), bcdNow()))
	f.Add(uint16(0x8604), true, cat(u32(3), u16(0), u16(3), u32(1), u32(1), u32(2), u32(2), u32(3), u32(1), u16(0)))
	f.Add(uint16(0x8606), true, cat(u32(4), u16(0), u16(2), u32(1), u32(1), u32(23120000), u32(113260000), []byte{30, 0}, u32(2), u32(2), u32(23130000), u32(113270000), []byte{30, 0}, u16(0)))
	f.Add(uint16(0x8606), false, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, id uint16, v19 bool, body []byte) {
		ids := []int{0x8600, 0x8602, 0x8604, 0x8606}
		msgID := ids[int(id)%len(ids)]
		ver := V2013
		if v19 {
			ver = V2019
		}
		_, list, err := parseAreas(msgID, ver, body)
		if err != nil {
			return
		}
		for _, a := range list {
			a.contains(23.12, 113.26)
		}
	})
}

// 其它带结构的下行：8108 升级包、8500 车辆控制、9208 报警附件上传
func FuzzDownBodies(f *testing.F) {
	f.Add(byte(0), true, []byte{})
	f.Add(byte(1), false, []byte{1, 0, 1})
	f.Add(byte(2), true, cat(lstr("127.0.0.1"), u16(7000), u16(0), make([]byte, 40), make([]byte, 32), make([]byte, 16)))
	f.Fuzz(func(t *testing.T, which byte, v19 bool, body []byte) {
		ver := V2013
		if v19 {
			ver = V2019
		}
		switch which % 3 {
		case 0:
			parse8108(ver, body)
		case 1:
			parse8500(ver, body)
		case 2:
			parse9208(ver, body)
		}
	})
}
