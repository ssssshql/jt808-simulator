package main

// 时间水印：生成附件图片 / 拍照画面时，把本地时间与媒体通道号烧进图像，便于联调时确认画面新鲜度与通道来源。
// 合成彩条图直接画在 RGBA 上；真实抓帧与上传图片先解码再画，解码失败按原样返回。
// 用内置 5×7 点阵字模，不引入字体库依赖。

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strconv"
	"time"
)

// font5x7 5×7 点阵字模：7 行、每行 5 列，'1' 为亮像素。只收录水印用到的字符。
var font5x7 = map[rune][7]string{
	'0': {"01110", "10001", "10011", "10101", "11001", "10001", "01110"},
	'1': {"00100", "01100", "00100", "00100", "00100", "00100", "01110"},
	'2': {"01110", "10001", "00001", "00010", "00100", "01000", "11111"},
	'3': {"11111", "00010", "00100", "00010", "00001", "10001", "01110"},
	'4': {"00010", "00110", "01010", "10010", "11111", "00010", "00010"},
	'5': {"11111", "10000", "11110", "00001", "00001", "10001", "01110"},
	'6': {"00110", "01000", "10000", "11110", "10001", "10001", "01110"},
	'7': {"11111", "00001", "00010", "00100", "01000", "01000", "01000"},
	'8': {"01110", "10001", "10001", "01110", "10001", "10001", "01110"},
	'9': {"01110", "10001", "10001", "01111", "00001", "00010", "01100"},
	'-': {"00000", "00000", "00000", "11111", "00000", "00000", "00000"},
	':': {"00000", "00100", "00100", "00000", "00100", "00100", "00000"},
	' ': {"00000", "00000", "00000", "00000", "00000", "00000", "00000"},
	'C': {"01110", "10001", "10000", "10000", "10000", "10001", "01110"},
	'H': {"10001", "10001", "10001", "11111", "10001", "10001", "10001"},
	'K': {"10001", "10010", "10100", "11000", "10100", "10010", "10001"},
	'M': {"10001", "11011", "10101", "10101", "10001", "10001", "10001"},
	'/': {"00001", "00010", "00010", "00100", "01000", "01000", "10000"},
	'.': {"00000", "00000", "00000", "00000", "00000", "01100", "01100"},
	',': {"00000", "00000", "00000", "00000", "00100", "00100", "01000"},
}

// stampText 水印文字：本地时间 + 媒体通道号（0 表示全部通道）
func stampText(ch int, at time.Time) string {
	return at.Format("2006-01-02 15:04:05") + " CH" + strconv.Itoa(ch)
}

// drawTextBox 在指定角画带半透明黑底的 5×7 点阵文字
func drawTextBox(dst *image.RGBA, text string, topLeft bool) {
	b := dst.Bounds()
	if b.Dx() < 60 || b.Dy() < 24 || text == "" {
		return
	}
	scale := max(1, min(min(b.Dx()/200, b.Dy()/150), 4))
	pad := 3 * scale
	tw := (len([]rune(text))*6 - 1) * scale
	th := 7 * scale
	x0, y0 := b.Min.X+pad, b.Max.Y-th-pad
	if topLeft {
		y0 = b.Min.Y + pad
	}
	for y := y0 - pad; y < y0+th+pad; y++ {
		for x := x0 - pad; x < x0+tw+pad; x++ {
			if !image.Pt(x, y).In(b) {
				continue
			}
			r, g, bl, _ := dst.At(x, y).RGBA()
			dst.Set(x, y, color.RGBA{uint8((r >> 8) * 45 / 100), uint8((g >> 8) * 45 / 100), uint8((bl >> 8) * 45 / 100), 255})
		}
	}
	px := x0
	for _, r := range text {
		g, ok := font5x7[r]
		if ok {
			for row := 0; row < 7; row++ {
				for col := 0; col < 5; col++ {
					if g[row][col] != '1' {
						continue
					}
					for dy := 0; dy < scale; dy++ {
						for dx := 0; dx < scale; dx++ {
							if x, y := px+col*scale+dx, y0+row*scale+dy; image.Pt(x, y).In(b) {
								dst.Set(x, y, color.RGBA{255, 255, 255, 255})
							}
						}
					}
				}
			}
		}
		px += 6 * scale
	}
}

// drawStamp 在图像左下角画时间水印（黑底白字，按分辨率自动缩放）
func drawStamp(dst *image.RGBA, ch int, at time.Time) {
	drawTextBox(dst, stampText(ch, at), false)
}

// stampJPEG 给已有 JPEG 烧入时间水印；解码或编码失败时返回原图（不因为水印影响附件生成）
func stampJPEG(data []byte, ch int, at time.Time) []byte {
	return stampJPEGWithOSD(data, ch, at, "")
}

// stampJPEGWithOSD 同时烧入左下角时间水印与左上角 OSD（速度/经纬度）
func stampJPEGWithOSD(data []byte, ch int, at time.Time, osd string) []byte {
	im, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return data
	}
	src := im.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, src.Dx(), src.Dy()))
	draw.Draw(dst, dst.Bounds(), im, src.Min, draw.Src)
	drawStamp(dst, ch, at)
	if osd != "" {
		drawTextBox(dst, osd, true)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 88}); err != nil {
		return data
	}
	return buf.Bytes()
}
