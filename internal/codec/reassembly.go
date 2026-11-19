package codec

// 下行分包重组：按消息 ID 收集各包，收齐后按包序号拼接；超时补传请求（0005）所需的缺失包列表

import (
	"fmt"
	"sort"
	"time"
)

// Partial 一条正在重组的分包消息
type Partial struct {
	ID, Total, First int            // 消息 ID、总包数、首包流水号
	Parts            map[int][]byte // 包序号 -> 消息体
	Since            time.Time      // 最近一次收到分包的时间
	Asks             int            // 已请求补传次数
	Last             *Msg           // 最近收到的一包
}

// MaxParts 单条消息允许的最大分包数（WORD 上限），用于拒绝明显异常的分包头
const MaxParts = 0xFFFF

// CheckPart 校验分包序号是否合法
func CheckPart(m *Msg) error {
	if m.Total <= 0 || m.Total > MaxParts || m.Index < 1 || m.Index > m.Total {
		return fmt.Errorf("%04X 分包序号非法：%d/%d", m.ID, m.Index, m.Total)
	}
	return nil
}

// Assembler 多条消息的分包重组表（非并发安全，调用方加锁）
type Assembler map[int]*Partial

// Add 加入一包；收齐时返回重组后的完整消息（Total/Index 置 0）并从表中移除，否则返回 nil。
// 同一消息 ID 的总包数变化时丢弃旧的重组进度重新开始
func (a Assembler) Add(m *Msg, now time.Time) (*Msg, error) {
	if err := CheckPart(m); err != nil {
		return nil, err
	}
	p := a[m.ID]
	if p == nil || p.Total != m.Total {
		p = &Partial{ID: m.ID, Total: m.Total, First: (m.Serial - m.Index + 1) & 0xFFFF, Parts: map[int][]byte{}}
		a[m.ID] = p
	}
	p.Parts[m.Index] = m.Body
	p.Since, p.Last = now, m
	if len(p.Parts) != p.Total {
		return nil, nil
	}
	delete(a, m.ID)
	full := *m
	full.Body, full.Total, full.Index = p.Join(), 0, 0
	return &full, nil
}

// Join 按包序号拼接全部分包
func (p *Partial) Join() []byte {
	var body []byte
	for i := 1; i <= p.Total; i++ {
		body = append(body, p.Parts[i]...)
	}
	return body
}

// Missing 尚未收到的包序号（升序，最多 limit 个；limit<=0 不限制）
func (p *Partial) Missing(limit int) []int {
	var miss []int
	for i := 1; i <= p.Total; i++ {
		if _, ok := p.Parts[i]; !ok {
			miss = append(miss, i)
			if limit > 0 && len(miss) >= limit {
				break
			}
		}
	}
	sort.Ints(miss)
	return miss
}

// ResendRequest 0005 补传分包请求消息体：首包流水号 + 重传包总数 + 重传包 ID 列表（最多 255 个）
func (p *Partial) ResendRequest() ([]byte, []int) {
	miss := p.Missing(255)
	body := Cat(U16(p.First), []byte{byte(len(miss))})
	for _, i := range miss {
		body = Cat(body, U16(i))
	}
	return body, miss
}
