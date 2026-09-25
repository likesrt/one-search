/**
 * 「使用文档」页的内容汇总入口。
 *
 * 每个大章节一个模块，这里只做顺序编排与导出。页面通过 `chapters` 渲染，
 * 因此新增章节时只需在此追加，视图侧无需改动。
 *
 * 内容事实来源：本章节内容以当前代码行为为准；若与仓库内其它 Markdown 文档冲突，
 * 以代码为准。已知与 `docs/mcp.md` 的差异已在「MCP 配置」章节内标注。
 */

import type { DocChapter } from './types'
import { quickstartChapter } from './quickstart'
import { consoleChapter } from './console'
import { searchApiChapter } from './search-api'
import { fetchChapter } from './fetch'
import { compatApiChapter } from './compat-api'
import { mcpChapter } from './mcp'
import { providersChapter } from './providers'
import { routingChapter } from './routing'
import { authQuotaChapter } from './auth-quota'
import { faqChapter } from './faq'

/** 全部章节，顺序即页面呈现顺序与左侧目录顺序。 */
export const chapters: DocChapter[] = [
  quickstartChapter,
  consoleChapter,
  searchApiChapter,
  fetchChapter,
  compatApiChapter,
  mcpChapter,
  providersChapter,
  routingChapter,
  authQuotaChapter,
  faqChapter
]

export type { DocBlock, DocChapter, DocSection, CalloutTone } from './types'
