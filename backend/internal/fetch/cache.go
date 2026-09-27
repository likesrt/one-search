package fetch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// defaultCacheDir 是缓存的默认目录（容器内绝对路径）。
	//
	// 刻意不挂卷：缓存是可以随时丢弃的数据，跨容器持久化没有收益，反而要求
	// Dockerfile 与 compose 同步改动。代价是容器重建后首批请求要重新打上游。
	defaultCacheDir = "/app/data/fetch-cache"
	// cacheDirEnv 是覆盖缓存目录的环境变量名，本地开发时指向仓库内的临时目录。
	cacheDirEnv = "FETCH_CACHE_DIR"
	// cacheFileSuffix 是缓存文件后缀。加后缀是为了让清扫任务能一眼区分缓存文件与
	// 写入过程中的临时文件，避免把别人正在写入的临时文件当垃圾删掉。
	cacheFileSuffix = ".json"
	// cacheTempPattern 是原子写入用的临时文件名模板。
	cacheTempPattern = "*.tmp"
	// cacheDirPerm 是缓存目录权限：仅本进程可读写，缓存内容可能包含敏感页面正文。
	cacheDirPerm = 0o700
	// cacheFilePerm 是缓存文件权限，与目录权限同源。
	cacheFilePerm = 0o600
)

// 通道标识，出现在 Result.Channel 与缓存条目里。
const (
	// ChannelDirect 表示内容来自内置抓取
	ChannelDirect = "direct"
	// ChannelTavily 表示内容来自 Tavily extract 回退
	ChannelTavily = "tavily"
)

// defaultFallbackMinChars 是触发回退的可见文本长度阈值默认值。
//
// 取 80 的依据是实测：整片失败的样本可见文本在 12–56 之间，而正常的短页面
// （example.com 131 字）不会被误触发，阈值落在这段空隙里。
const defaultFallbackMinChars = 80

// cacheErrorStatuses 是使用短 TTL（cache_error_ttl_seconds）的上游状态码集合。
//
// 只有这三者：它们是「内容型门禁结果」—— 拿到的是对方的限流或质询页面，
// 缓存下来能避免几分钟内反复撞门禁，过期后自动重试。404/5xx 不缓存为错误态
// 特例（仍按正常 TTL 处理），因为它们的正文本身就是有效内容。
var cacheErrorStatuses = map[int]bool{401: true, 403: true, 429: true}

// CacheEntry 是一条抓取缓存，字段与磁盘上的 JSON 一一对应。
//
// 之所以缓存「完整内容 + 来源」而不是切好的片段：续读（start_index > 0）可以直接从
// 同一份内容切片，零网络请求，且 total_length 与续读提示与首次完全一致。
type CacheEntry struct {
	// URL 是本次抓取的地址，仅用于排错时人工识别文件内容
	URL string `json:"url"`
	// Method 是实际使用的出站方法，仅用于排错
	Method string `json:"method"`
	// StoredAt 是写入时刻，仅用于排错；过期判定一律以文件 mtime 为准
	StoredAt time.Time `json:"stored_at"`
	// StatusCode 是内置抓取拿到的上游状态码（可能为 4xx/5xx）
	StatusCode int `json:"status_code"`
	// Channel 是最终采用哪条通道，取值为 ChannelDirect 或 ChannelTavily
	Channel string `json:"channel"`
	// ContentType 是最终通道的 Content-Type
	ContentType string `json:"content_type"`
	// Content 是归一化后、截断前的完整内容
	Content string `json:"content"`
}

// DefaultCacheDir 返回抓取缓存目录。
//
// 优先读环境变量 FETCH_CACHE_DIR（本地开发指向仓库内临时目录），为空时返回
// 容器内的默认绝对路径。返回值恒为非空字符串，是否真正可用由调用方在写入时决定。
// 无参数、无副作用。
func DefaultCacheDir() string {
	if dir := strings.TrimSpace(os.Getenv(cacheDirEnv)); dir != "" {
		return dir
	}
	return defaultCacheDir
}

// cacheKey 计算缓存键：sha256(url | method | body | headers | raw)。
//
// 必须带上 method/body/headers，否则两个不同的 POST 请求会互相污染缓存
// （同一 URL 带不同 body 是常见用法，只按 URL 缓存会返回错误内容）。
// headers 覆盖默认 UA，因此也要进键，否则换 UA 抓同一页面会命中上一次的结果。
//
// 参数 req 为已规范化的请求；返回值是 64 位十六进制字符串，可直接当文件名。
// 本函数为纯函数，不做 IO。
func cacheKey(req Request) string {
	fields := []string{
		req.URL.String(),
		req.Method,
		req.Body,
		headersFingerprint(req.Headers),
		fmt.Sprintf("%t", req.Raw),
	}
	sum := sha256.Sum256([]byte(strings.Join(fields, "\x00")))
	return hex.EncodeToString(sum[:])
}

// headersFingerprint 把请求头拼成稳定字符串。
//
// map 的遍历顺序是随机的，不排序会让同一组请求头算出不同键，缓存永远命不中。
// 分隔符用 \x00：请求头名与值都不允许含该字符，不会产生歧义拼接。
func headersFingerprint(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"\x01"+headers[key])
	}
	return strings.Join(parts, "\x00")
}

// cachePath 把缓存键映射为文件路径。
//
// hash 与文件名分离（目录固定 + 哈希文件名）是有意的：直接拿 URL 当文件名会让
// 路径分隔符、查询串与超长地址变成文件名问题，哈希后长度与字符集都恒定。
func cachePath(dir string, key string) string {
	return filepath.Join(dir, key+cacheFileSuffix)
}

// readCache 读取缓存条目并在过期时顺手删除。
//
// 参数 dir 为空或 key 为空时直接未命中。TTL 判定见 cacheExpired：
// 过期条目会被删除（读时清理只是补充，与访问无关的清扫由 CleanCache 负责，
// 否则一个再也没人访问的文件永远不会被删）。
//
// 返回值：命中的条目与 true；未命中、文件损坏或已过期时返回零值与 false。
// 副作用：可能删除一个过期或损坏的缓存文件；失败只记日志。
func readCache(dir string, key string, normalTTL int, errorTTL int) (CacheEntry, bool) {
	if dir == "" || key == "" {
		return CacheEntry{}, false
	}
	path := cachePath(dir, key)
	info, err := os.Stat(path)
	if err != nil {
		return CacheEntry{}, false
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		log.Printf("fetch: 读取缓存 %s 失败: %v", path, err)
		return CacheEntry{}, false
	}
	var entry CacheEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		// 损坏的条目不当作命中：宁可重新抓一次，也不要把半个 JSON 当正文返回给模型。
		log.Printf("fetch: 缓存 %s 解析失败，已丢弃: %v", path, err)
		removeCacheFile(path)
		return CacheEntry{}, false
	}
	if now := time.Now(); cacheExpired(entry.StatusCode, info.ModTime(), now, normalTTL, errorTTL) {
		removeCacheFile(path)
		return CacheEntry{}, false
	}
	return entry, true
}

// cacheExpired 判断一条缓存是否过期。
//
// 时间基准取文件 mtime 而非条目里的 StoredAt，理由是与 CleanCache 的清扫口径一致：
// 若两处用不同基准，会出现「读时认为有效、清扫时认为已过期」的分歧。
// 复制或备份还原会改变 mtime，那种场景下按新 mtime 重新计时是可接受的。
//
// 边界条件：TTL 非正视为立即过期（cache_ttl_seconds=0 即关闭缓存的语义）；
// modified 为零值时按已过期处理，避免一个时间戳异常的文件被永久命中。
// 返回 true 表示应当视为未命中。
func cacheExpired(statusCode int, modified time.Time, now time.Time, normalTTL int, errorTTL int) bool {
	ttl := cacheTTLFor(statusCode, normalTTL, errorTTL)
	if ttl <= 0 || modified.IsZero() {
		return true
	}
	return now.Sub(modified) >= time.Duration(ttl)*time.Second
}

// cacheTTLFor 返回某状态码对应的缓存时长（秒）。
//
// 4xx 门禁类状态码（401/403/429）用较短的 errorTTL：避免几分钟内反复撞门禁，
// 又不至于把一次限流结果长期钉死。errorTTL 非正时回落到 normalTTL，
// 使「只配了正常 TTL」的配置不会意外得到「错误态不缓存」的行为。
func cacheTTLFor(statusCode int, normalTTL int, errorTTL int) int {
	if !cacheErrorStatuses[statusCode] {
		return normalTTL
	}
	if errorTTL > 0 {
		return errorTTL
	}
	return normalTTL
}

// writeCache 原子写入一条缓存。
//
// 先写同目录下的临时文件再 rename：rename 在同一文件系统内是原子的，
// 因此读侧永远看不到半个文件（直接覆盖写会有一段「文件存在但内容不全」的窗口，
// 并发读到的就是损坏条目）。
//
// 参数 maxBytes 为单条缓存上限（按文件字节数算），<=0 表示不限；超过时不写，
// 返回 ErrCacheTooLarge，由调用方决定是否记日志。目录不存在时按需创建。
//
// 返回值：写入成功返回 nil；超限返回 ErrCacheTooLarge；其余为 IO 或序列化错误。
// 副作用：创建目录与文件，并更新 mtime（过期判定的依据）。
func writeCache(dir string, key string, entry CacheEntry, maxBytes int64) error {
	if dir == "" || key == "" {
		return nil
	}
	if entry.StoredAt.IsZero() {
		entry.StoredAt = time.Now()
	}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if maxBytes > 0 && int64(len(payload)) > maxBytes {
		return ErrCacheTooLarge
	}
	if err := os.MkdirAll(dir, cacheDirPerm); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, cacheTempPattern)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		removeCacheFile(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		removeCacheFile(tempPath)
		return err
	}
	// 权限收紧到 0600：CreateTemp 给的是 0600，但显式设置可避免被 umask 之外的因素放宽。
	if err := os.Chmod(tempPath, cacheFilePerm); err != nil {
		removeCacheFile(tempPath)
		return err
	}
	return os.Rename(tempPath, cachePath(dir, key))
}

// removeCacheFile 删除缓存文件，失败只记日志。
//
// 清理动作永远不是主流程的必要条件，因此不向调用方传播错误；
// 但必须留日志，否则「缓存莫名不生效」会变成无法排查的问题。
func removeCacheFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("fetch: 删除缓存 %s 失败: %v", path, err)
	}
}

// cacheFile 是清扫时用到的一个缓存文件的元信息。
type cacheFile struct {
	path     string
	modified time.Time
	size     int64
}

// CleanCache 清扫抓取缓存目录：先删过期条目，再按 mtime 从旧到新删到总量阈值以下。
//
// 与访问无关的清扫路径是必需的：只靠读时删除的话，一个抓取过之后再没人访问的
// URL，其缓存文件永远不会被读到，也就永远不会被删除，磁盘长期只涨不跌。
//
// 参数：
//   - dir：缓存目录，为空时直接返回（视为未启用缓存）。
//   - normalTTL / errorTTL：与读路径同源的两种 TTL（秒），用于判定「过期」。
//   - maxTotalBytes：目录总量上限，<=0 表示不做总量淘汰。
//
// 返回值：遍历目录失败时返回错误；单个文件删除失败只记日志（一次清扫不应因为
// 一个删不掉的文件而整体失败）。
//
// 副作用：删除缓存文件；目录不存在时直接返回 nil，不创建目录。
func CleanCache(dir string, normalTTL int, errorTTL int, maxTotalBytes int64) error {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	now := time.Now()
	kept := make([]cacheFile, 0, len(entries))
	total := int64(0)
	for _, item := range entries {
		if item.IsDir() || !strings.HasSuffix(item.Name(), cacheFileSuffix) {
			continue
		}
		path := filepath.Join(dir, item.Name())
		info, err := item.Info()
		if err != nil {
			continue
		}
		if cacheExpired(cacheStatusOf(path), info.ModTime(), now, normalTTL, errorTTL) {
			removeCacheFile(path)
			continue
		}
		kept = append(kept, cacheFile{path: path, modified: info.ModTime(), size: info.Size()})
		total += info.Size()
	}
	removed := sweepOldest(kept, total, maxTotalBytes)
	if removed > 0 {
		log.Printf("fetch: 缓存总量超限，按 mtime 淘汰 %d 个文件", removed)
	}
	return nil
}

// cacheStatusOf 读出缓存文件里的上游状态码，供清扫时判定该条用哪种 TTL。
//
// 只声明 status_code 一个字段来反序列化：完整解码会把每条正文（最大 3MB）
// 都变成字符串分配，而清扫每小时跑一次且只关心「是 401/403/429 还是一般内容」。
//
// 边界条件：文件缺失、损坏或字段缺失都返回 0，此时按一般内容处理
// （即用 normalTTL），未过期的条目因此不会因为读不出状态码而被误删。
func cacheStatusOf(path string) int {
	payload, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var meta struct {
		StatusCode int `json:"status_code"`
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return 0
	}
	return meta.StatusCode
}

// sweepOldest 在总量超过上限时按 mtime 从旧到新删除，返回删除的文件数。
//
// 参数 files 为待淘汰集合（本函数会原地排序，调用方不应再依赖其顺序），
// total 为其字节总量，maxTotalBytes <= 0 时不做任何删除。
//
// 已知取舍：淘汰只看 mtime，可能删掉仍在 TTL 内的条目（极端流量下）。
// 用精确 LRU 需要维护独立的访问时间索引，对本场景不值得。
func sweepOldest(files []cacheFile, total int64, maxTotalBytes int64) int {
	if maxTotalBytes <= 0 || total <= maxTotalBytes {
		return 0
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	removed := 0
	for _, file := range files {
		if total <= maxTotalBytes {
			break
		}
		removeCacheFile(file.path)
		total -= file.size
		removed++
	}
	return removed
}
