/**
 * 「使用文档」页的内容数据结构。
 *
 * 设计约束：本页不引入任何 Markdown 渲染依赖，正文以结构化数据描述，
 * 由 `DocBlockList.vue` 按块类型渲染。因此每种块都是可穷举的字面量联合，
 * 新增块类型时 TypeScript 会在渲染组件处报错，避免内容与渲染脱节。
 */

/** 提示框的语气，决定配色：info 中性 / warn 注意 / danger 危险 / success 通过。 */
export type CalloutTone = 'info' | 'warn' | 'danger' | 'success'

/**
 * 文档内容块。
 *
 * - paragraph：普通段落，行内 `反引号` 片段会渲染为等宽代码样式
 * - heading：小节内的小标题，默认 h3，可用 level 降到 h4
 * - list：无序/有序列表，items 每项都支持行内反引号
 * - code：代码块，带语言与可选标题，右上角提供复制按钮
 * - table：表格，columns 为表头，rows 每行长度应与表头一致
 * - callout：语气提示框
 * - steps：步骤列表，每步有标题与说明
 * - divider：分隔线
 */
export type DocBlock =
  | { type: 'paragraph'; text: string }
  | { type: 'heading'; text: string; level?: 3 | 4 }
  | { type: 'list'; ordered?: boolean; items: string[] }
  | { type: 'code'; lang: string; title?: string; content: string }
  | { type: 'table'; columns: string[]; rows: string[][] }
  | { type: 'callout'; tone: CalloutTone; title?: string; text: string }
  | { type: 'steps'; items: { title: string; text: string }[] }
  | { type: 'divider' }

/** 文档小节。id 用于锚点跳转与滚动高亮，全页必须唯一。 */
export interface DocSection {
  id: string
  title: string
  blocks: DocBlock[]
}

/** 文档大章节，对应左侧目录的一级项。id 同样需要全页唯一。 */
export interface DocChapter {
  id: string
  title: string
  sections: DocSection[]
}
