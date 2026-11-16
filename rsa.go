package main

// 消息体 RSA 加密（消息体属性 bit10）：
//   - 平台公钥由 8A00 下发并保存；终端公钥用 0A00 上报（e DWORD + n BYTE[128]）
//   - 下行 bit10=1：用终端私钥解密后再处理
//   - 上行：配置「上行RSA加密」打开且已收到平台公钥时，用平台公钥加密消息体并置 bit10
//
// 标准只规定了 bit10 表示 RSA，没有规定填充与分组。这里采用业内常见做法：
// RSA 1024 位 + PKCS#1 v1.5 填充，明文按 117 字节分组，每组加密为 128 字节密文依次拼接；
// 解密按 128 字节分组。终端密钥对随终端状态保存（state/<phone>.json），重启后不变。

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"math/big"
)

const (
	rsaBits     = 1024
	rsaBlock    = rsaBits / 8   // 128 字节密文分组
	rsaPlainMax = rsaBlock - 11 // PKCS#1 v1.5 每组最多 117 字节明文
)

// PlatKey 平台 RSA 公钥（8A00）
type PlatKey struct {
	E int    `json:"e"`
	N string `json:"n"` // hex
}

func (k PlatKey) pub() *rsa.PublicKey {
	if k.E == 0 || k.N == "" {
		return nil
	}
	b, err := hex.DecodeString(k.N)
	if err != nil || len(b) == 0 {
		return nil
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(b), E: k.E}
}

// termRSA 终端私钥（首次使用时生成并随状态保存）。调用方持有 t.mu
func (t *Terminal) termRSA() *rsa.PrivateKey {
	if t.rsaKey != nil {
		return t.rsaKey
	}
	if b, err := hex.DecodeString(t.RSAKey); err == nil && len(b) > 0 {
		if k, err := x509.ParsePKCS1PrivateKey(b); err == nil {
			t.rsaKey = k
			return k
		}
	}
	k, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil
	}
	t.rsaKey = k
	t.RSAKey = hex.EncodeToString(x509.MarshalPKCS1PrivateKey(k))
	t.markDirty()
	return k
}

// rsaEncrypt 分组加密（117 字节明文 -> 128 字节密文）
func rsaEncrypt(pub *rsa.PublicKey, plain []byte) ([]byte, error) {
	size := pub.Size()
	max := size - 11
	var out []byte
	for i := 0; i < len(plain); i += max {
		c, err := rsa.EncryptPKCS1v15(rand.Reader, pub, plain[i:min(i+max, len(plain))])
		if err != nil {
			return nil, err
		}
		out = append(out, c...)
	}
	return out, nil
}

// rsaDecrypt 分组解密（每 128 字节一组）
func rsaDecrypt(key *rsa.PrivateKey, cipher []byte) ([]byte, error) {
	if key == nil {
		return nil, fmt.Errorf("终端 RSA 私钥不可用")
	}
	size := key.Size()
	if len(cipher) == 0 || len(cipher)%size != 0 {
		return nil, fmt.Errorf("密文长度 %d 不是 %d 的整数倍", len(cipher), size)
	}
	var out []byte
	for i := 0; i < len(cipher); i += size {
		p, err := rsa.DecryptPKCS1v15(nil, key, cipher[i:i+size])
		if err != nil {
			return nil, fmt.Errorf("第 %d 组解密失败: %v", i/size+1, err)
		}
		out = append(out, p...)
	}
	return out, nil
}

// encryptUp 上行加密：返回（可能加密后的）消息体与是否已加密。调用方持有 t.mu
func (t *Terminal) encryptUp(id int, body []byte) ([]byte, bool) {
	if !t.Cfg.UpRSA || id == 0x0A00 || len(body) == 0 {
		return body, false // 0A00 携带终端公钥本身，不加密；空消息体无需加密
	}
	pub := t.PlatRSA.pub()
	if pub == nil {
		return body, false
	}
	c, err := rsaEncrypt(pub, body)
	if err != nil {
		return body, false
	}
	return c, true
}
