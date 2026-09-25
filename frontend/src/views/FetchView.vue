<template>
  <div class="fetch-page">
    <div class="page-hd">
      <div>
        <h1>网页抓取</h1>
        <p class="page-sub">抓取任意 URL · HTML 转紧凑 Markdown · 按字符数截断并可续读</p>
      </div>
      <div class="page-actions">
        <el-button :disabled="loading" @click="load">刷新</el-button>
      </div>
    </div>

    <PageSkeleton v-if="loading && !settings" type="form" />
    <div v-else-if="settings" v-loading="loading" class="fetch-stack">
      <!-- 抓取调试：主用途，置顶 -->
      <section class="soft-card fetch-card">
        <div class="sec-hd">
          <div>
            <h2>抓取调试</h2>
            <p>与对外接口 <code>GET|POST /v1/fetch</code> 行为一致，参数与 MCP 工具相同</p>
          </div>
          <el-tag v-if="!settings.enabled" type="info" effect="plain" round>功能已关闭</el-tag>
        </div>

        <div class="search-row">
          <el-input
            v-model="form.url"
            class="search-input"
            placeholder="https://example.com/article"
            :disabled="!settings.enabled"
            @keyup.enter="run"
          />
          <el-button
            type="primary"
            class="search-btn"
            :loading="fetching"
            :disabled="!settings.enabled || !form.url.trim()"
            @click="run"
          >
            抓取
          </el-button>
        </div>

        <div class="chips">
          <label class="chip">
            <span>长度</span>
            <el-input-number
              v-model="form.max_length"
              class="chip-number"
              size="small"
              :min="1"
              :max="50000"
              controls-position="right"
              :disabled="!settings.enabled"
            />
          </label>
          <label class="chip">
            <span>方法</span>
            <el-select v-model="form.method" class="chip-select" size="small" :disabled="!settings.enabled">
              <el-option value="GET" label="GET" />
              <el-option value="POST" label="POST" />
            </el-select>
          </label>
          <label class="chip">
            <span>原始内容</span>
            <el-switch v-model="form.raw" size="small" :disabled="!settings.enabled" />
          </label>
          <!-- 续读起点只对 GET 有意义：POST 响应无法续读，后端会直接拒绝 -->
          <label v-if="form.method === 'GET'" class="chip">
            <span>起点</span>
            <el-input-number
              v-model="form.start_index"
              class="chip-number"
              size="small"
              :min="0"
              controls-position="right"
              :disabled="!settings.enabled"
            />
          </label>
          <button class="chip chip-reset" type="button" :disabled="!settings.enabled" @click="resetForm">
            重置参数
          </button>
        </div>

        <div v-if="form.method === 'POST'" class="post-fields">
          <div class="field">
            <label>请求头 (JSON 对象，可留空)</label>
            <el-input v-model="form.headers" type="textarea" :rows="3" placeholder='{"Accept": "application/json"}' />
          </div>
          <div class="field">
            <label>请求体 (字符串或 JSON 值)</label>
            <el-input v-model="form.body" type="textarea" :rows="3" placeholder='{"q": "关键词"}' />
          </div>
        </div>
      </section>

      <!-- 抓取结果 -->
      <section v-if="result || errorText" class="soft-card fetch-card">
        <div class="sec-hd">
          <div>
            <h2>结果</h2>
            <p v-if="result">
              状态码 {{ result.status_code }} · {{ result.content_type || '未声明 Content-Type' }} ·
              {{ result.total_length }} 字符
            </p>
            <p v-else>抓取失败</p>
          </div>
          <div class="result-actions">
            <el-button v-if="result?.truncated" size="small" @click="continueReading">续读</el-button>
            <el-button v-if="result" size="small" @click="copyText(result.content)">复制内容</el-button>
          </div>
        </div>

        <div v-if="errorText" class="error-banner">{{ errorText }}</div>
        <template v-else-if="result">
          <div class="pills">
            <span class="pill mono">{{ result.method }} {{ result.url }}</span>
            <span v-if="result.truncated" class="pill warn-pill">
              已截断 · 下一段从 {{ result.next_start_index }} 开始
            </span>
          </div>
          <pre class="code-box result-box">{{ result.content }}</pre>
        </template>
      </section>

      <!-- 配置：只 4 个字段，不值得上页内 tab -->
      <section class="soft-card fetch-card">
        <div class="sec-hd">
          <div>
            <h2>配置</h2>
            <p>本功能专属的全局配置，保存后对新请求立即生效</p>
          </div>
          <el-tag :type="settings.enabled ? 'primary' : 'info'" effect="plain" round>
            {{ settings.enabled ? '已启用' : '已关闭' }}
          </el-tag>
        </div>

        <div class="field-grid">
          <div class="field field-switch">
            <div>
              <label>启用网页抓取</label>
              <span class="hint">关闭后 /v1/fetch 返回 404，MCP 也不再列出 fetch 工具</span>
            </div>
            <el-switch v-model="settings.enabled" />
          </div>
          <div class="field">
            <label>抓取超时 (ms)</label>
            <el-input-number v-model="settings.timeout_ms" :min="1" :max="60000" :step="1000" controls-position="right" />
            <span class="hint">上界 60000：反向代理读取超时为 65s，配得更长会先被断开</span>
          </div>
          <div class="field field-full">
            <label>抓取代理</label>
            <el-input v-model="settings.proxy_url" placeholder="留空表示直连，例如 http://127.0.0.1:7890" />
            <span class="hint">
              留空直连；容器部署时 127.0.0.1 / localhost 会被自动改写为 host.docker.internal。
              代理地址由管理员配置、视为可信，但仍会拦截内网目标。
            </span>
          </div>
          <div class="field field-switch field-full">
            <div>
              <label>放行内网目标</label>
              <span class="hint">允许抓取 127.0.0.1、10.x、192.168.x 等内网地址</span>
            </div>
            <el-switch v-model="settings.allow_private" />
          </div>
        </div>

        <div v-if="settings.allow_private" class="warn-banner">
          放行内网目标后，任何持有接口令牌的调用方都能借本服务探测你的内网（SSRF）。
          仅在完全可信的内网部署、且确实需要抓取内部服务时开启。
        </div>

        <div class="config-actions">
          <el-button :disabled="!dirty" @click="discard">放弃更改</el-button>
          <el-button type="primary" :disabled="!dirty" :loading="saving" @click="save">保存配置</el-button>
        </div>
      </section>

      <div class="bottom-space" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus/es/components/message/index'
import PageSkeleton from '../components/PageSkeleton.vue'
import { api, FetchResult, FetchSettings } from '../api/client'

const loading = ref(true)
const saving = ref(false)
const fetching = ref(false)
const settings = ref<FetchSettings>()
const savedSnapshot = ref('')
const result = ref<FetchResult>()
const errorText = ref('')

const form = reactive({
  url: '',
  method: 'GET',
  max_length: 5000,
  start_index: 0,
  raw: false,
  headers: '',
  body: ''
})

/** 配置是否有未保存更改；以服务端回包序列化结果作为基线，避免默认值补齐造成误判。 */
const dirty = computed(() => {
  if (!settings.value || !savedSnapshot.value) return false
  return serialize(settings.value) !== savedSnapshot.value
})

/**
 * 归一化配置后再序列化，用于脏检查比较。
 *
 * timeout_ms 缺省时补 30000：后端读不到配置行时会返回内置默认值，但历史数据可能缺字段，
 * 不补齐会让「看起来没改」的配置被判为脏。
 * @param value 当前配置
 * @returns 稳定的 JSON 字符串
 */
function serialize(value: FetchSettings): string {
  return JSON.stringify({
    enabled: Boolean(value.enabled),
    proxy_url: (value.proxy_url || '').trim(),
    allow_private: Boolean(value.allow_private),
    timeout_ms: Number(value.timeout_ms) || 30000
  })
}

/**
 * 用服务端回包重置脏检查基线（照 SettingsView.save 的 rememberSaved 模式）。
 * @param next 服务端返回的配置
 */
function rememberSaved(next: FetchSettings) {
  settings.value = { ...next }
  savedSnapshot.value = serialize(next)
}

/** 加载抓取配置与默认参数。首屏失败时提示错误并保持空态。 */
async function load() {
  loading.value = true
  try {
    rememberSaved(await api.fetchSettings())
  } catch (error) {
    ElMessage.error((error as Error).message)
  } finally {
    loading.value = false
  }
}

/** 放弃未保存的配置改动，回滚到上一次服务端回包的状态。 */
function discard() {
  if (!savedSnapshot.value) return
  settings.value = JSON.parse(savedSnapshot.value) as FetchSettings
}

/** 重置抓取参数到默认值；URL 保留，便于改了参数后重抓同一地址。 */
function resetForm() {
  form.method = 'GET'
  form.max_length = 5000
  form.start_index = 0
  form.raw = false
  form.headers = ''
  form.body = ''
}

/**
 * 校验 URL：必须是带 host 的 http/https 绝对地址。
 *
 * 与后端 parseRequestURL 对齐（裸域名一律拒绝，不做 scheme 静默补全），
 * 提前给出提示而不是等接口返回 400。
 * @param value 输入框中的地址
 * @returns 错误提示文案；校验通过时返回空串
 */
function urlError(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) return '请输入要抓取的 URL'
  try {
    const parsed = new URL(trimmed)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return 'URL 必须以 http:// 或 https:// 开头'
    }
    if (!parsed.host) return 'URL 缺少主机名'
    return ''
  } catch {
    return 'URL 需是完整地址，例如 https://example.com（不支持省略 https://）'
  }
}

/**
 * 把请求头输入框解析为对象。
 *
 * 空串表示不传请求头；非空但解析失败或不是对象时报错，避免把非法内容静默丢弃。
 * @param raw 输入框原文
 * @returns 解析出的请求头对象（可为 undefined）；非法时返回错误文案
 */
function parseHeaders(raw: string): { headers?: Record<string, unknown>; error?: string } {
  const trimmed = raw.trim()
  if (!trimmed) return {}
  try {
    const parsed = JSON.parse(trimmed)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      return { error: '请求头需是 JSON 对象' }
    }
    return { headers: parsed as Record<string, unknown> }
  } catch {
    return { error: '请求头不是合法 JSON' }
  }
}

/**
 * 把请求体输入框解析为可发送的值。
 *
 * 空串表示不传请求体（POST 可以带空 body）；能解析为 JSON 时按 JSON 值发送，
 * 否则按原始字符串发送（后端两者都接受）。
 * @param raw 输入框原文
 * @returns 请求体值；空串时返回 undefined 表示不携带
 */
function parseBody(raw: string): unknown {
  const trimmed = raw.trim()
  if (!trimmed) return undefined
  try {
    return JSON.parse(trimmed)
  } catch {
    return raw
  }
}

/**
 * 组装一次抓取的请求参数。
 *
 * 只放后端支持的字段（url/method/headers/body/max_length/start_index/raw）；
 * proxy 刻意不传 —— 代理是管理员级全局配置，客户端无法指定。
 * @returns 可直接发送给 /api/admin/fetch/test 的参数对象；校验失败时返回错误文案
 */
function buildPayload(): { payload?: Record<string, unknown>; error?: string } {
  const payload: Record<string, unknown> = {
    url: form.url.trim(),
    method: form.method,
    max_length: Number(form.max_length) || 5000,
    raw: form.raw
  }
  if (form.method === 'GET') {
    payload.start_index = Number(form.start_index) || 0
    return { payload }
  }
  const { headers, error } = parseHeaders(form.headers)
  if (error) return { error }
  if (headers) payload.headers = headers
  // 空 body 是合法输入（后端用 hasBody 区分），因此只有非空才带上该字段
  const body = parseBody(form.body)
  if (body !== undefined) payload.body = body
  return { payload }
}

/** 执行一次抓取：先做命令式校验（早退 + 警告），再请求管理台试抓接口。 */
async function run() {
  if (!settings.value?.enabled) {
    ElMessage.warning('抓取功能已关闭，请先在下方配置中启用')
    return
  }
  const invalid = urlError(form.url)
  if (invalid) {
    ElMessage.warning(invalid)
    return
  }
  const { payload, error } = buildPayload()
  if (error || !payload) {
    ElMessage.warning(error || '参数不合法')
    return
  }
  fetching.value = true
  errorText.value = ''
  try {
    result.value = await api.testFetch(payload)
  } catch (caught) {
    result.value = undefined
    errorText.value = (caught as Error).message
  } finally {
    fetching.value = false
  }
}

/**
 * 按上一次结果里的 next_start_index 继续读取下一段。
 *
 * 续读会重放请求，因此只对 GET 开放：POST 可能创建资源或二次计费，
 * 后端也会拒绝带 start_index 的 POST，这里提前拦住避免无意义的往返。
 */
async function continueReading() {
  if (!result.value) return
  if (form.method !== 'GET') {
    ElMessage.warning('POST 响应无法续读，请提高最大长度后重抓')
    return
  }
  form.start_index = result.value.next_start_index
  await run()
}

/**
 * 复制文本到剪贴板；内容为空时直接返回，避免写入空串。
 * @param text 待复制文本
 */
async function copyText(text: string) {
  if (!text) return
  await navigator.clipboard.writeText(text)
  ElMessage.success('已复制')
}

/** 保存配置：命令式校验（早退 + 警告）后用服务端回包重置脏检查基线。 */
async function save() {
  if (!settings.value || !dirty.value) return
  if (settings.value.timeout_ms < 1 || settings.value.timeout_ms > 60000) {
    ElMessage.warning('超时必须在 1 到 60000 毫秒之间')
    return
  }
  saving.value = true
  try {
    rememberSaved(await api.updateFetchSettings(settings.value))
    ElMessage.success('配置已保存')
  } catch (error) {
    ElMessage.error((error as Error).message)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.fetch-page {
  max-width: 920px;
  margin: 0 auto;
  padding-bottom: 24px;
}
/* page-sub 不是全局类，需在此自定义 */
.page-sub {
  margin: 4px 0 0;
  color: var(--muted);
  font-size: 13px;
}
.fetch-stack {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.fetch-card {
  padding: 16px 18px 18px;
}
.sec-hd {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}
.sec-hd h2 {
  margin: 0;
  font-size: 16px;
  letter-spacing: -0.02em;
}
.sec-hd p {
  margin: 4px 0 0;
  color: var(--muted);
  font-size: 12px;
  line-height: 1.5;
}
.sec-hd code {
  font-family: var(--mono);
}

.search-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: #fff;
  transition: border-color 0.2s ease, box-shadow 0.2s ease;
}
.search-row:focus-within {
  border-color: #b7e4d2;
  box-shadow: 0 1px 2px rgba(16, 24, 40, 0.04), 0 12px 32px rgba(11, 110, 79, 0.1);
}
.search-input {
  flex: 1;
  min-width: 0;
}
.search-input :deep(.el-input__wrapper) {
  box-shadow: none !important;
  background: transparent;
}
.search-btn {
  height: 38px;
  border-radius: 10px !important;
  padding: 0 20px;
  font-weight: 650;
}

.chips {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  background: #f6f7f9;
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 4px 10px 4px 12px;
  font-size: 12px;
  color: #475467;
  font-weight: 600;
}
.chip-select {
  width: 92px;
}
.chip-select :deep(.el-select__wrapper) {
  box-shadow: none !important;
  background: transparent;
  min-height: 28px;
  padding: 0 4px 0 0;
}
.chip-number {
  width: 108px;
}
.chip-number :deep(.el-input__wrapper) {
  box-shadow: none !important;
  background: transparent;
  padding-left: 0;
}
.chip-reset {
  cursor: pointer;
  padding: 4px 12px;
  font-family: inherit;
}
.chip-reset:hover:not(:disabled) {
  border-color: #cfe8dc;
  color: var(--primary-ink);
}
.chip-reset:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}

.post-fields {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-top: 12px;
}

.pills {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 10px;
  font-size: 12px;
  color: var(--muted);
}
.pill {
  background: #fff;
  border: 1px solid var(--border);
  border-radius: 999px;
  padding: 4px 10px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.warn-pill {
  background: var(--warn-soft);
  border-color: #fedf89;
  color: var(--warn);
}
.result-actions {
  display: flex;
  gap: 8px;
  flex-shrink: 0;
}
.result-box {
  margin: 0;
  max-height: 420px;
  font-size: 12.5px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
}

.error-banner,
.warn-banner {
  padding: 10px 12px;
  border-radius: 12px;
  font-size: 12px;
  line-height: 1.5;
  word-break: break-word;
}
.error-banner {
  background: var(--danger-soft);
  border: 1px solid #fecdca;
  color: var(--danger);
}
.warn-banner {
  margin-top: 12px;
  background: var(--warn-soft);
  border: 1px solid #fedf89;
  color: var(--warn);
}

.field-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: 14px;
  background: #fbfcfd;
  min-width: 0;
}
.field-full {
  grid-column: 1 / -1;
}
.field label {
  font-size: 12px;
  font-weight: 700;
  color: var(--muted);
}
.field .hint {
  display: block;
  font-size: 12px;
  font-weight: 400;
  color: var(--muted);
  line-height: 1.5;
}
.field :deep(.el-input-number) {
  width: 100%;
}
.field-switch {
  flex-direction: row;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}
.config-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 14px;
}
.bottom-space {
  height: 24px;
}

@media (max-width: 980px) {
  .field-grid,
  .post-fields {
    grid-template-columns: 1fr;
  }
  .search-row {
    flex-direction: column;
    align-items: stretch;
  }
  .search-btn {
    width: 100%;
  }
  .sec-hd {
    flex-direction: column;
  }
  .result-actions,
  .config-actions {
    width: 100%;
  }
  .result-actions :deep(.el-button),
  .config-actions :deep(.el-button) {
    flex: 1;
  }
}
</style>
