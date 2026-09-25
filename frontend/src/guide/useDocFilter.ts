/**
 * 「使用文档」页的关键字过滤。
 *
 * 拆成多个小纯函数而非一个组件内的大函数，便于单独推理与测试；
 * 视图侧只需把 `chapters` 与输入关键字交给 `filterChapters`。
 *
 * 过滤范围：章节标题、小节标题，以及小节内所有块的文本
 * （段落文案、小标题、列表项、代码内容与代码标题、表格单元格、提示框、步骤）。
 */

import type { DocBlock, DocChapter, DocSection } from './types'

/**
 * 把单个内容块拍平成可检索的纯文本。
 *
 * 表格会展开表头与全部单元格；代码块会带上标题与正文；
 * `divider` 无文本，返回空串。
 *
 * @param block 任意内容块
 * @returns 该块的纯文本，多个片段以空格分隔
 */
export function blockPlainText(block: DocBlock): string {
  switch (block.type) {
    case 'paragraph':
      return block.text
    case 'heading':
      return block.text
    case 'list':
      return block.items.join(' ')
    case 'code':
      return `${block.title || ''} ${block.content}`
    case 'table':
      return [block.columns.join(' '), ...block.rows.map((row) => row.join(' '))].join(' ')
    case 'callout':
      return `${block.title || ''} ${block.text}`
    case 'steps':
      return block.items.map((item) => `${item.title} ${item.text}`).join(' ')
    default:
      // divider 无文本；同时兜住未来新增块类型，避免过滤时抛错。
      return ''
  }
}

/**
 * 把小节拍平成可检索的纯文本，包含小节标题本身。
 *
 * @param section 文档小节
 * @returns 小节标题与全部块文本拼接结果
 */
export function sectionPlainText(section: DocSection): string {
  return [section.title, ...section.blocks.map(blockPlainText)].join(' ')
}

/**
 * 大小写不敏感的「空关键字视为不过滤」子串匹配。
 *
 * @param text 待检索文本
 * @param keyword 已经预处理（trim + 小写）的关键字
 * @returns 命中返回 true；keyword 为空串时恒为 true
 */
export function matchesKeyword(text: string, keyword: string): boolean {
  if (!keyword) return true
  return text.toLowerCase().includes(keyword)
}

/**
 * 按关键字过滤单个小节：标题或其任一内容块命中即保留整节。
 *
 * 保留整节而不是只留有命中的块，是为了让命中上下文仍然可读
 * （例如搜 `brave` 时表格与紧随其后的易错点说明应一起出现）。
 *
 * @param section 文档小节
 * @param keyword 已预处理的关键字
 * @returns 命中时返回原小节，否则返回 null
 */
export function filterSection(section: DocSection, keyword: string): DocSection | null {
  if (matchesKeyword(sectionPlainText(section), keyword)) return section
  return null
}

/**
 * 按关键字过滤整份文档。
 *
 * 关键字为空（或仅空白）时直接返回原数组，保持「清空搜索即恢复全量」；
 * 小节全部被过滤掉的章节不会出现在结果里，因此不会留下空章节标题。
 *
 * @param chapters 全量章节
 * @param keyword 用户输入的关键字，可为空
 * @returns 过滤后的章节数组；返回的是新数组，章节与小节对象本身复用原引用
 */
export function filterChapters(chapters: DocChapter[], keyword: string): DocChapter[] {
  const needle = keyword.trim().toLowerCase()
  if (!needle) return chapters
  const result: DocChapter[] = []
  for (const chapter of chapters) {
    const sections = chapter.sections
      .map((section) => filterSection(section, needle))
      .filter((section): section is DocSection => section !== null)
    if (sections.length === 0) continue
    result.push({ id: chapter.id, title: chapter.title, sections })
  }
  return result
}

/**
 * 计算用于滚动高亮与目录点击的小节锚点列表。
 *
 * 章节标题本身不作为锚点：目录点击章节时跳到它的第一个小节，
 * 这样左侧目录只需要维护一份 id 顺序。
 *
 * @param chapters 当前展示的章节（通常已过滤）
 * @returns 按文档顺序排列的小节 id 数组
 */
export function collectSectionIds(chapters: DocChapter[]): string[] {
  return chapters.flatMap((chapter) => chapter.sections.map((section) => section.id))
}
