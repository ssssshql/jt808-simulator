package main

import (
	"encoding/hex"
	"math"
	"strconv"
)

func itoa(n int) string { return strconv.Itoa(n) }

func ftoa(f float64, prec int) string { return strconv.FormatFloat(f, 'f', prec, 64) }

// distM 两点距离（米）
func distM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := p2-p1, (lng2-lng1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Sqrt(a))
}

// bearing 方位角（0=正北，顺时针）
func bearing(lat1, lng1, lat2, lng2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dl := (lng2 - lng1) * math.Pi / 180
	y := math.Sin(dl) * math.Cos(p2)
	x := math.Cos(p1)*math.Sin(p2) - math.Sin(p1)*math.Cos(p2)*math.Cos(dl)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

// moveBy 沿方位角移动 d 米
func moveBy(lat, lng, dir, d float64) (float64, float64) {
	rad := dir * math.Pi / 180
	lat2 := lat + d*math.Cos(rad)/111320
	lng2 := lng + d*math.Sin(rad)/(111320*math.Cos(lat*math.Pi/180))
	return lat2, lng2
}

func angleDiff(a, b float64) float64 {
	d := math.Abs(math.Mod(a-b+540, 360) - 180)
	return d
}

// inPolygon 射线法；pts 为 [lat, lng]
func inPolygon(lat, lng float64, pts [][2]float64) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		yi, xi, yj, xj := pts[i][0], pts[i][1], pts[j][0], pts[j][1]
		if (yi > lat) != (yj > lat) && lng < (xj-xi)*(lat-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// hexBytes hex 串转定长字节（不足补 0）
func hexBytes(h string, n int) []byte {
	b, _ := hex.DecodeString(h)
	out := make([]byte, n)
	copy(out, b)
	return out
}
