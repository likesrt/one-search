/**
 * 文档示例里的地址替换。
 *
 * 手册中的 curl / 配置示例统一以 `DOC_BASE_ORIGIN` 书写，避免写死某一个部署地址：
 * 开发态前端跑在 5173 并代理 `/api`、`/v1`（见 `vite.config.ts`），
 * all-in-one 态则是同源。因此把示例里的该前缀换成 `window.location.origin`
 * 在两种部署下都能直接复制使用；用户也可以关掉开关，看到原始示例文本。
 */

/** 示例内容中使用的占位地址前缀，必须与各章节内容里书写的地址保持一致。 */
export const DOC_BASE_ORIGIN = 'http://localhost:5173'

/**
 * 把示例文本里的占位地址替换为实际站点地址。
 *
 * 纯字符串切分拼接，不使用正则，避免地址里的 `.`、`/` 被当成元字符；
 * 也不用 `replaceAll`，以兼容更老的构建目标。
 *
 * @param text 原始示例文本（段落、表格单元格或代码块内容）
 * @param origin 实际站点地址，通常取 `window.location.origin`
 * @param enabled 是否启用替换；为 false 时原样返回
 * @returns 替换后的文本；`origin` 为空或与占位地址相同时原样返回
 */
export function replaceDocOrigin(text: string, origin: string, enabled: boolean): string {
  if (!enabled || !origin || origin === DOC_BASE_ORIGIN) return text
  return text.split(DOC_BASE_ORIGIN).join(origin)
}
