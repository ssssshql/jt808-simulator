package main

// 状态文件的持久化：写临时文件 → fsync → 旧文件改名为 .bak → 临时文件改名为正式文件 → fsync 目录。
// 读取时正式文件缺失或损坏（JSON 无法解析）则回退到 .bak，避免掉电 / 强杀后状态清零。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// writeFileDurable 原子且持久地写入 path；成功后上一份正常文件保留为 path.bak
func writeFileDurable(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	// 只有当前正式文件是可解析的 JSON 时才轮换为 .bak，免得一份坏文件覆盖掉好的备份
	if old, err := os.ReadFile(path); err == nil && json.Valid(old) {
		if err := os.Rename(path, path+".bak"); err != nil {
			return fmt.Errorf("备份旧文件失败: %w", err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir 把目录项（改名）刷到磁盘；Windows 不支持对目录 fsync，忽略错误
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// readJSONDurable 读取 path 并解析到 v；正式文件不存在或解析失败时尝试 path.bak。
// 返回实际使用的文件（path 或 path.bak）；两者都不可用时返回第一个错误（文件都不存在时为 fs.ErrNotExist）
func readJSONDurable(path string, v any) (string, error) {
	var first error
	for _, p := range []string{path, path + ".bak"} {
		b, err := os.ReadFile(p)
		if err == nil {
			if err = json.Unmarshal(b, v); err == nil {
				return p, nil
			}
			err = fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if first == nil || errors.Is(first, fs.ErrNotExist) && !errors.Is(err, fs.ErrNotExist) {
			first = err
		}
	}
	return "", first
}
