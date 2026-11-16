package main

import (
	"os"
	"time"
)

// protoZone JT/T 808 规定报文中的时间（BCD[6] YYMMDDhhmmss）一律为 GMT+8。
// 模拟器常部署在时区为 UTC 的服务器上，若直接用系统时区，0200/0201/0702/0801 等报文的时间会慢 8 小时，
// 平台下发的时间（8802/8803 检索区间、区域起止时间、9208 报警时间等）也会被错误解释。
// 因此进程启动时把本地时区固定为 GMT+8；如确需其它时区，可设环境变量 JT808_TZ（IANA 名称，如 UTC）。
var protoZone = time.FixedZone("CST", 8*3600)

func init() {
	if name := os.Getenv("JT808_TZ"); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			time.Local = loc
			return
		}
	}
	time.Local = protoZone
}
