package main

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

// locAttrs 解析 0200 附加信息
func locAttrs(body []byte) map[int][]byte {
	out := map[int][]byte{}
	for p := 28; p+2 <= len(body); {
		id, n := int(body[p]), int(body[p+1])
		if p+2+n > len(body) {
			break
		}
		out[id] = body[p+2 : p+2+n]
		p += 2 + n
	}
	return out
}

func TestLocTiresCabinTemp(t *testing.T) {
	for _, ver := range []string{"2019", "2013"} {
		p := newFakePlat(t, map[string]int{"2019": V2019, "2013": V2013}[ver])
		term := newTestTerm(t, p, ver)
		if err := term.Do("setVehicle", map[string]any{"tires": "250, 240,0", "cabinTemp": "-18"}); err != nil {
			t.Fatal(err)
		}
		term.reportNow()
		m := p.expect(0x0200, 5*time.Second, func(m *Msg) bool { return len(m.Body) > 28 })
		a := locAttrs(m.Body)
		if ver == "2013" {
			if a[0x05] != nil || a[0x06] != nil {
				t.Fatal("2013 不应附带 0x05/0x06")
			}
			continue
		}
		if len(a[0x05]) != 30 || a[0x05][0] != 250 || a[0x05][1] != 240 || a[0x05][2] != 0 || a[0x05][3] != 0xFF || a[0x05][29] != 0xFF {
			t.Fatalf("0x05 %x", a[0x05])
		}
		if int16(binary.BigEndian.Uint16(a[0x06])) != -18 {
			t.Fatalf("0x06 %x", a[0x06])
		}
		// 清空后不再附带
		term.Do("setVehicle", map[string]any{"tires": "", "cabinTemp": ""})
		term.reportNow()
		m = p.expect(0x0200, 5*time.Second, func(m *Msg) bool { return len(m.Body) > 28 })
		if a := locAttrs(m.Body); a[0x05] != nil || a[0x06] != nil {
			t.Fatal("清空后不应附带")
		}
		for _, bad := range []map[string]any{{"tires": "255"}, {"tires": strings.Repeat("1,", 31)}, {"cabinTemp": "abc"}} {
			if term.Do("setVehicle", bad) == nil {
				t.Errorf("%v 应报错", bad)
			}
		}
	}
}

func TestPassthroughTypes(t *testing.T) {
	p := newFakePlat(t, V2019)
	term := newTestTerm(t, p, "2019")
	if err := term.Do("send", map[string]any{"msgId": "0900", "params": map[string]any{"type": float64(0x41), "hex": "01 02 7E"}}); err != nil {
		t.Fatal(err)
	}
	if m := p.expect(0x0900, 5*time.Second, nil); !bytes.Equal(m.Body, []byte{0x41, 1, 2, 0x7E}) {
		t.Fatalf("%x", m.Body)
	}
	// 0x00：NMEA，校验和正确
	term.Do("send", map[string]any{"msgId": "0900", "params": map[string]any{"type": float64(0), "content": ""}})
	m := p.expect(0x0900, 5*time.Second, nil)
	s := string(m.Body[1:])
	if m.Body[0] != 0 || !strings.HasPrefix(s, "$GPGGA,") || !strings.Contains(s, "\r\n$GPRMC,") {
		t.Fatalf("%q", s)
	}
	for _, line := range strings.Split(strings.TrimSpace(s), "\r\n") {
		star := strings.LastIndex(line, "*")
		var c byte
		for i := 1; i < star; i++ {
			c ^= line[i]
		}
		if want := strings.ToUpper(line[star+1:]); want != strings.ToUpper(hex2(c)) {
			t.Fatalf("NMEA 校验和 %s != %02X", want, c)
		}
	}
	// 8900 0x0B IC 卡认证 -> 0001 + 0900 0x0B（64 字节）
	ser := p.send(0x8900, append([]byte{0x0B}, make([]byte, 24)...))
	if r := p.ackResult(ser); r != 0 {
		t.Fatal(r)
	}
	m = p.expect(0x0900, 5*time.Second, nil)
	if m.Body[0] != 0x0B || len(m.Body) != 65 {
		t.Fatalf("%x", m.Body)
	}
	if nmea(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), -23.5, -113.25, 18.52, 90, 10, 9)[:20] != "$GPGGA,030405.00,233" {
		t.Fatal(nmea(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), -23.5, -113.25, 18.52, 90, 10, 9))
	}
}

func hex2(c byte) string { return string("0123456789ABCDEF"[c>>4]) + string("0123456789ABCDEF"[c&15]) }
