import { nextTick, onBeforeUnmount, onMounted, ref, watch, type Ref } from 'vue'

/** 滚动高亮的可选项。 */
export interface ScrollSpyOptions {
  /** 观察带的 rootMargin，默认把「视口顶部往下 88px 到 30% 高度」作为命中带。 */
  rootMargin?: string
}

/**
 * 用 IntersectionObserver 跟踪各小节标题，返回当前应高亮的小节 id。
 *
 * 为什么用观察带而不是 scroll 事件：内容区有数十个小节，监听 scroll 每帧计算
 * `getBoundingClientRect` 会造成无谓的布局抖动；IntersectionObserver 只在
 * 跨越边界时回调。
 *
 * @param sectionIds 按文档顺序排列的小节 id（响应式）；顺序变化或被过滤后会重新观察
 * @param options 观察带配置
 * @returns `activeId` 当前高亮小节 id（初始与无命中时为第一个小节）；
 *          `refresh` 供内容异步渲染后手动重建观察器
 */
export function useScrollSpy(sectionIds: Ref<string[]>, options: ScrollSpyOptions = {}) {
  const activeId = ref('')
  const visibleIds = new Set<string>()
  let observer: IntersectionObserver | null = null

  /** 在可见集合中按文档顺序取第一个作为高亮项，保证多节同时可见时不跳来跳去。 */
  function syncActive() {
    const next = sectionIds.value.find((id) => visibleIds.has(id))
    if (next) activeId.value = next
  }

  /** IntersectionObserver 回调：维护可见集合后同步高亮。 */
  function handleEntries(entries: IntersectionObserverEntry[]) {
    for (const entry of entries) {
      const id = entry.target.id
      if (!id) continue
      if (entry.isIntersecting) visibleIds.add(id)
      else visibleIds.delete(id)
    }
    syncActive()
  }

  /** 断开观察器并清空可见集合，可重复调用。 */
  function disconnect() {
    if (observer) observer.disconnect()
    observer = null
    visibleIds.clear()
  }

  /**
   * 重建观察器。
   * 过滤导致小节列表变化、或首次挂载时 DOM 尚未就绪，都需要重新取元素；
   * 高亮项若已不在当前列表中则回退到第一个小节，避免目录停留在已消失的章节上。
   */
  async function refresh() {
    disconnect()
    await nextTick()
    if (typeof IntersectionObserver === 'undefined') {
      // 老浏览器兜底：目录仍可点击跳转，只是不做滚动高亮。
      activeId.value = sectionIds.value[0] || ''
      return
    }
    observer = new IntersectionObserver(handleEntries, {
      rootMargin: options.rootMargin ?? '-88px 0px -70% 0px',
      threshold: 0
    })
    for (const id of sectionIds.value) {
      const element = document.getElementById(id)
      if (element) observer.observe(element)
    }
    if (!activeId.value || !sectionIds.value.includes(activeId.value)) {
      activeId.value = sectionIds.value[0] || ''
    }
  }

  onMounted(() => {
    void refresh()
  })

  watch(sectionIds, () => {
    void refresh()
  })

  onBeforeUnmount(disconnect)

  return { activeId, refresh }
}
