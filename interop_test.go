package main

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"
)

// 报文时间一律为 GMT+8，与服务器系统时区无关（JT/T 808 时间字段约定）
func TestBCDTimeIsGMT8(t *testing.T) {
	if _, off := time.Now().Zone(); off != 8*3600 {
		t.Fatalf("本地时区偏移 %d，应为 GMT+8", off)
	}
	utc := time.Date(2026, 10, 31, 14, 17, 10, 0, time.UTC)
	if got := hex.EncodeToString(bcdTime(utc)); got != "261031221710" {
		t.Fatalf("bcdTime(UTC 14:17:10) = %s，应为 261031221710", got)
	}
	r := newReader(bcd("261031221710", 6))
	if got := r.time6(); !got.Equal(utc) {
		t.Fatalf("time6 = %v，应为 %v", got, utc)
	}
}

// jt808-server（yezhihao）2019 版 8500 少了“控制类型数量”：类型ID WORD + 参数 BYTE（1=加锁）
func TestParse8500JT808ServerFormat(t *testing.T) {
	res, err := parse8500(V2019, []byte{0, 1, 1})
	if err != nil || !res.lock || !res.legacy {
		t.Fatalf("加锁: %+v %v", res, err)
	}
	res, err = parse8500(V2019, []byte{0, 1, 0})
	if err != nil || res.lock {
		t.Fatalf("解锁: %+v %v", res, err)
	}
	res, err = parse8500(V2019, []byte{0xF0, 1, 1})
	if err != nil || len(res.unsupported) != 1 || res.unsupported[0] != 0xF001 {
		t.Fatalf("不支持类型: %+v %v", res, err)
	}
	// 标准格式不受影响：数量 1 + 车门 0x0001 + 0 锁闭
	res, err = parse8500(V2019, bytes.Join([][]byte{{0, 1}, {0, 1}, {0}}, nil))
	if err != nil || !res.lock || res.legacy {
		t.Fatalf("标准格式: %+v %v", res, err)
	}
}
