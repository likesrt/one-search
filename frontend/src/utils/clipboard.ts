/**
 * 跨环境复制文本到剪贴板。
 *
 * `navigator.clipboard` 只在安全上下文（HTTPS / localhost）下存在，而本项目的
 * 一体化部署默认是 nginx 监听 80 端口，用户多半通过 `http://<IP>:5173` 访问管理台，
 * 此时 `navigator.clipboard` 为 `undefined`，直接调用会抛
 * "Cannot read properties of undefined (reading 'writeText')"。
 * 因此这里保留一条 `execCommand('copy')` 降级路径：它不受安全上下文限制，
 * 仅仅是被标记为废弃，故只在首选方案不可用或失败时才走。
 *
 * @param text 待写入剪贴板的文本
 * @returns 写入成功返回 true；空文本或两条路径都失败返回 false，
 *          调用方据此提示用户手动选择文本，而不是静默失败
 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (!text) return false
  // 首选异步剪贴板 API。它除了安全上下文，还要求文档处于聚焦状态，
  // 因此即便 API 存在也可能 reject；这种情况继续走降级路径，
  // 避免用户点了「复制」却毫无反应。
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      /* 落入下方降级路径 */
    }
  }
  return fallbackCopy(text)
}

/**
 * 降级复制：造一个屏幕外文本域，选中后执行 `execCommand('copy')`。
 *
 * 必须挂到 document 上：未插入文档树的元素无法被 `select()` 真正选中，
 * 选中会静默失败，而 `execCommand` 仍可能返回 true，造成「提示成功但没复制」。
 * 用 `position: fixed` + `opacity: 0` 而不是 `display: none`，原因同上 ——
 * 不可见（display 为 none）的元素同样选不中。
 *
 * @param text 待复制文本
 * @returns `execCommand` 是否报告复制成功
 */
function fallbackCopy(text: string): boolean {
  const textarea = document.createElement('textarea')
  textarea.value = text
  // 只读可避免移动端弹出软键盘，也避免复制过程中内容被输入法改写
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.top = '0'
  textarea.style.left = '0'
  textarea.style.opacity = '0'
  document.body.appendChild(textarea)
  try {
    textarea.select()
    // iOS Safari 上 select() 对部分元素不生效，setSelectionRange 兜底
    textarea.setSelectionRange(0, textarea.value.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(textarea)
  }
}
