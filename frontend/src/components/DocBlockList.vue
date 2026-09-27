<template>
  <div class="doc-blocks">
    <template v-for="(block, index) in blocks" :key="index">
      <p v-if="block.type === 'paragraph'" class="doc-p">
        <template v-for="(part, partIndex) in inlineParts(block.text)" :key="partIndex">
          <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
          <template v-else>{{ part.text }}</template>
        </template>
      </p>

      <component
        :is="block.level === 4 ? 'h4' : 'h3'"
        v-else-if="block.type === 'heading'"
        class="doc-h"
        :class="{ 'is-sub': block.level === 4 }"
      >
        {{ resolveText(block.text) }}
      </component>

      <ul v-else-if="block.type === 'list' && !block.ordered" class="doc-list">
        <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
          <template v-for="(part, partIndex) in inlineParts(item)" :key="partIndex">
            <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
            <template v-else>{{ part.text }}</template>
          </template>
        </li>
      </ul>

      <ol v-else-if="block.type === 'list'" class="doc-list doc-list-ordered">
        <li v-for="(item, itemIndex) in block.items" :key="itemIndex">
          <template v-for="(part, partIndex) in inlineParts(item)" :key="partIndex">
            <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
            <template v-else>{{ part.text }}</template>
          </template>
        </li>
      </ol>

      <figure v-else-if="block.type === 'code'" class="doc-code">
        <figcaption class="doc-code-hd">
          <span class="doc-code-title">{{ block.title || block.lang }}</span>
          <button class="doc-copy" type="button" @click="copyCode(block.content, index)">
            {{ copiedIndex === index ? '已复制' : '复制' }}
          </button>
        </figcaption>
        <pre class="code-box doc-pre"><code>{{ resolveText(block.content) }}</code></pre>
      </figure>

      <div v-else-if="block.type === 'table'" class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr>
              <th v-for="(column, columnIndex) in block.columns" :key="columnIndex">
                <template v-for="(part, partIndex) in inlineParts(column)" :key="partIndex">
                  <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
                  <template v-else>{{ part.text }}</template>
                </template>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(row, rowIndex) in block.rows" :key="rowIndex">
              <td v-for="(column, columnIndex) in block.columns" :key="columnIndex">
                <template v-for="(part, partIndex) in inlineParts(tableCell(row, columnIndex))" :key="partIndex">
                  <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
                  <template v-else>{{ part.text }}</template>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="block.type === 'callout'" class="doc-callout" :class="`tone-${block.tone}`">
        <strong v-if="block.title" class="doc-callout-title">{{ block.title }}</strong>
        <p class="doc-callout-text">
          <template v-for="(part, partIndex) in inlineParts(block.text)" :key="partIndex">
            <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
            <template v-else>{{ part.text }}</template>
          </template>
        </p>
      </div>

      <ol v-else-if="block.type === 'steps'" class="doc-steps">
        <li v-for="(step, stepIndex) in block.items" :key="stepIndex" class="doc-step">
          <span class="doc-step-idx" aria-hidden="true">{{ stepIndex + 1 }}</span>
          <div class="doc-step-body">
            <strong class="doc-step-title">
              <template v-for="(part, partIndex) in inlineParts(step.title)" :key="partIndex">
                <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
                <template v-else>{{ part.text }}</template>
              </template>
            </strong>
            <p class="doc-step-text">
              <template v-for="(part, partIndex) in inlineParts(step.text)" :key="partIndex">
                <code v-if="part.code" class="doc-inline">{{ part.text }}</code>
                <template v-else>{{ part.text }}</template>
              </template>
            </p>
          </div>
        </li>
      </ol>

      <hr v-else-if="block.type === 'divider'" class="doc-hr" />
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * 文档内容块渲染器：按 `DocBlock.type` 逐类渲染。
 *
 * 安全约束：全程使用文本插值，**不使用 `v-html`**，因此文档内容无法注入 HTML；
 * 行内代码靠 `inlineParts` 按反引号切分后插值，不需要任何 HTML 解析。
 *
 * 状态只有「哪个代码块刚被复制」一项，且以块序号标识；内容块本身无内部状态，
 * 所以过滤导致块列表变化时不会残留脏状态。
 */
import { ref } from 'vue'
import { ElMessage } from 'element-plus/es/components/message/index'
import type { DocBlock } from '../guide/types'
import { replaceDocOrigin } from '../guide/origin'
import { copyToClipboard } from '../utils/clipboard'

const props = defineProps<{
  /** 当前小节的内容块，按顺序渲染 */
  blocks: DocBlock[]
  /** 实际站点地址，用于替换示例里的占位地址 */
  origin: string
  /** 是否启用地址替换；关闭时展示原始示例文本 */
  useOrigin: boolean
}>()

/** 行内代码片段：code 为 true 表示该片段位于一对反引号之间。 */
interface InlinePart {
  code: boolean
  text: string
}

const copiedIndex = ref(-1)
let copiedTimer: number | undefined

/**
 * 应用地址替换规则。
 * 除代码块外，段落、列表、表格单元格、提示框与步骤文案也会一并替换，
 * 避免同一示例在不同块里显示成两种地址。
 *
 * @param text 原始文本
 * @returns 替换后的文本；未启用替换时原样返回
 */
function resolveText(text: string): string {
  return replaceDocOrigin(text, props.origin, props.useOrigin)
}

/**
 * 把一段文本按反引号切分为交替的「普通文本 / 行内代码」片段，并顺带应用地址替换。
 *
 * 地址替换放在这里而不是各调用点，是因为行内代码片段（如 `` `http://localhost:5173/mcp` ``）
 * 同样需要替换 —— 之前的调用点各自处理容易漏掉这一支。替换是幂等的，
 * 表格正文经由 `tableCell` 取值后也走本函数，不会出现替换两次的差异。
 *
 * 以奇数下标判定代码片段，因此落单的反引号只会让它之后的内容整体变成代码样式，
 * 不会抛错，也不会吞掉字符。空片段直接跳过，避免渲染出多余节点。
 *
 * @param text 原始文本（可含 0 到多对反引号）
 * @returns 片段数组，顺序与原文一致，每段文本均已应用地址替换
 */
function inlineParts(text: string): InlinePart[] {
  const parts: InlinePart[] = []
  text.split('`').forEach((segment, index) => {
    if (!segment) return
    parts.push({ code: index % 2 === 1, text: resolveText(segment) })
  })
  return parts
}

/**
 * 读取表格某一行的第 index 个单元格原始文本。
 * 行长度短于表头时补空串，长于表头时多余单元格不渲染，保证表格结构不塌。
 *
 * @param row 表格行
 * @param index 列下标
 * @returns 单元格文本；地址替换由 `inlineParts` 统一完成，这里不做替换
 */
function tableCell(row: string[], index: number): string {
  return row[index] ?? ''
}

/**
 * 复制代码块内容到剪贴板，并短暂显示「已复制」。
 *
 * 剪贴板在非安全上下文（例如通过 IP 直接访问 http）不可用，copyToClipboard 内部
 * 已降级到 execCommand 兼容路径；连降级也失败时才提示用户手动选择文本，
 * 而不是静默失败。计时器只在自己仍是当前标记时清除，
 * 连点多个代码块时不会互相覆盖状态。
 *
 * @param content 代码块原始内容（未经地址替换，复制的是可直接使用的原文本）
 * @param index 代码块在 blocks 中的序号，用于标记按钮状态
 */
async function copyCode(content: string, index: number): Promise<void> {
  const ok = await copyToClipboard(content)
  if (!ok) {
    ElMessage.warning('浏览器未授权剪贴板，请手动选择代码文本')
    return
  }
  copiedIndex.value = index
  if (copiedTimer !== undefined) window.clearTimeout(copiedTimer)
  copiedTimer = window.setTimeout(() => {
    if (copiedIndex.value === index) copiedIndex.value = -1
  }, 1600)
}
</script>

<style scoped>
.doc-blocks { min-width: 0; }

.doc-p {
  margin: 0 0 10px;
  font-size: 14px;
  line-height: 1.75;
  color: var(--text);
  overflow-wrap: anywhere;
}

.doc-inline {
  padding: 1px 5px;
  margin: 0 1px;
  border-radius: 6px;
  border: 1px solid var(--border);
  background: #f6f7f9;
  font-family: var(--mono);
  font-size: 12.5px;
  color: #344054;
  overflow-wrap: anywhere;
}

.doc-h {
  margin: 22px 0 10px;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: -0.01em;
  color: var(--text);
}
.doc-h.is-sub {
  margin: 16px 0 8px;
  font-size: 13.5px;
  color: #344054;
}

.doc-list {
  margin: 0 0 12px;
  padding-left: 20px;
  font-size: 14px;
  line-height: 1.75;
  color: var(--text);
}
.doc-list li { margin-bottom: 4px; overflow-wrap: anywhere; }
.doc-list-ordered { list-style: decimal; }

.doc-code {
  position: relative;
  margin: 0 0 14px;
  border: 1px solid var(--border);
  border-radius: 12px;
  overflow: hidden;
  background: var(--card);
}
.doc-code-hd {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  padding: 7px 10px 7px 12px;
  border-bottom: 1px solid var(--border);
  background: #fafbfc;
}
.doc-code-title {
  font-family: var(--mono);
  font-size: 11.5px;
  font-weight: 600;
  color: var(--muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.doc-copy {
  flex: 0 0 auto;
  padding: 3px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  background: var(--card);
  color: var(--muted);
  font-size: 11.5px;
  font-weight: 600;
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s, background 0.15s;
}
.doc-copy:hover,
.doc-copy:focus-visible {
  color: var(--primary-ink);
  border-color: #b7e4d2;
  background: var(--primary-soft);
}

.doc-pre {
  margin: 0;
  padding: 12px 14px;
  border-radius: 0;
  font-size: 12.5px;
  line-height: 1.65;
  tab-size: 2;
}

.doc-table-wrap {
  margin: 0 0 14px;
  overflow-x: auto;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--card);
  overscroll-behavior-x: contain;
}
.doc-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
  min-width: 520px;
}
.doc-table th,
.doc-table td {
  padding: 8px 12px;
  text-align: left;
  vertical-align: top;
  border-bottom: 1px solid var(--border);
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.doc-table th {
  background: #fafbfc;
  font-weight: 700;
  font-size: 12px;
  color: var(--muted);
  white-space: nowrap;
}
.doc-table tbody tr:last-child td { border-bottom: 0; }

.doc-callout {
  margin: 0 0 14px;
  padding: 11px 14px;
  border-radius: 12px;
  border: 1px solid var(--border);
  border-left-width: 3px;
  background: #fafbfc;
  font-size: 13.5px;
  line-height: 1.7;
}
.doc-callout-title {
  display: block;
  margin-bottom: 3px;
  font-size: 13px;
}
.doc-callout-text { margin: 0; color: #344054; overflow-wrap: anywhere; }

.doc-callout.tone-info { border-left-color: #7dceb0; background: #f7fbf9; }
.doc-callout.tone-info .doc-callout-title { color: var(--primary-ink); }
.doc-callout.tone-success { border-left-color: #12b76a; background: #f6fef9; }
.doc-callout.tone-success .doc-callout-title { color: #067647; }
.doc-callout.tone-warn { border-left-color: #f79009; background: var(--warn-soft); border-color: #fedf89; }
.doc-callout.tone-warn .doc-callout-title { color: var(--warn); }
.doc-callout.tone-danger { border-left-color: #f04438; background: var(--danger-soft); border-color: #fecdca; }
.doc-callout.tone-danger .doc-callout-title { color: var(--danger); }

.doc-steps {
  margin: 0 0 14px;
  padding: 0;
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.doc-step {
  display: flex;
  align-items: flex-start;
  gap: 10px;
}
.doc-step-idx {
  flex: 0 0 auto;
  width: 22px;
  height: 22px;
  margin-top: 1px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: var(--primary-soft);
  color: var(--primary-ink);
  font-size: 12px;
  font-weight: 800;
  font-variant-numeric: tabular-nums;
}
.doc-step-body { min-width: 0; }
.doc-step-title {
  display: block;
  font-size: 13.5px;
  color: var(--text);
}
.doc-step-text {
  margin: 3px 0 0;
  font-size: 13px;
  line-height: 1.7;
  color: var(--muted);
  overflow-wrap: anywhere;
}

.doc-hr {
  margin: 20px 0;
  border: 0;
  border-top: 1px solid var(--border);
}

@media (max-width: 980px) {
  .doc-table { min-width: 460px; }
  .doc-pre { font-size: 12px; }
}
</style>
