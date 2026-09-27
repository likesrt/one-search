package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

type Crypto struct {
	key []byte
}

func NewCrypto(secret string) *Crypto {
	sum := sha256.Sum256([]byte(secret))
	return &Crypto{key: sum[:]}
}

func (c *Crypto) Encrypt(plain string) (string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (c *Crypto) Decrypt(encoded string) (string, error) {
	payload, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext is too short")
	}
	nonce := payload[:gcm.NonceSize()]
	ciphertext := payload[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TokenPrefix(token string) string {
	trimmed := strings.TrimSpace(token)
	if len(trimmed) <= 8 {
		return trimmed
	}
	return trimmed[:8]
}

// MaskSecret 生成密钥的脱敏展示串，用于列表页在不解密的前提下让用户辨认是哪把 key。
//
// 规则：纯空白（含空串）返回空串；去空白后长度 <= 8 返回 "****"；否则保留首尾各 4 个字符、中间替换为 "****"。
// 空值之所以不返回 "****"，是因为脱敏不是校验：网关允许用空密钥条目表达「无密钥调用」，
// 而 "****" 会让「匿名」与「已配置但看不清」在管理台上无法区分（两者进一步会落到不同的调用路径）。
// 数据层同样以空串作为匿名判据，这里返回空串可让 key_hint 为空自然成立，无需额外标志位。
// 参数 secret 允许为任意字符串（不做长度校验，超长值只截取首尾）；返回值恒非 nil，无副作用。
func MaskSecret(secret string) string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 8 {
		return "****"
	}
	return trimmed[:4] + "****" + trimmed[len(trimmed)-4:]
}

func RandomToken(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
