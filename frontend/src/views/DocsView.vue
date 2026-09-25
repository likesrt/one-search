<template>
  <div class="docs-page">
    <div class="page-hd">
      <div>
        <h1>使用文档</h1>
        <p class="page-sub">从配置到接入的完整手册 · 描述与当前代码行为一致</p>
      </div>
      <div class="page-actions docs-actions">
        <el-input
          v-model="keyword"
          class="docs-search"
          clearable
          placeholder="搜索关键字，如 brave / fallback / 401"
          :prefix-icon="Search"
        />
        <el-switch v-model="useOrigin" active-text="使用当前站点地址" />
      </div>
    </div>

    <div class="docs-layout">
      <nav class="docs-nav soft-card" aria-label="文档目录">
        <el-select v-model="activeId" class="docs-nav-select" placeholder="跳转到章节">
          <el-option v-for="item in navItems" :key="item.id" :label="item.label" :value="item.id" />
        </el-select>

        <div class="docs-nav-list">
          <div v-for="chapter in visibleChapters" :key="chapter.id" class="docs-nav-chapter">
            <button
              type="button"
              class="docs-nav-chapter-title"
              @click="jumpTo(chapter.sections[0]?.id)"
            >
              {{ chapter.title }}
            </button>
            <button
              v-for="section in chapter.sections"
              :key="section.id"
              type="button"
              class="docs-nav-link"
              :class="{ on: section.id === activeId }"
              @click="jumpTo(section.id)"
            >
              {{ section.title }}
            </button>
          </div>
        </div>
      </nav>

      <main class="docs-content">
        <section v-for="chapter in visibleChapters" :key="chapter.id" class="docs-chapter">
          <h2 class="docs-chapter-title">{{ chapter.title }}</h2>
          <article
            v-for="section in chapter.sections"
            :id="section.id"
            :key="section.id"
            class="docs-section soft-card"
          >
            <h3 class="docs-section-title">{{ section.title }}</h3>
            <DocBlockList :blocks="section.blocks" :origin="origin" :use-origin="useOrigin" />
          </article>
        </section>

        <div v-if="!visibleChapters.length" class="docs-empty soft-card">
          <strong>没有匹配的内容</strong>
          <p class="muted">换一个关键字试试，例如 provider 名称、`fallback`、`401`、`base_url`。清空搜索框可恢复全量目录。</p>
        </div>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 「使用文档」页：全站唯一的使用手册入口。
 *
 * 页面本身不发任何网络请求，内容全部来自 `src/guide/` 下的结构化数据，
 * 因此不需要骨架屏，也没有加载失败态。脚本只做装配：
 * 过滤（`useDocFilter`）+ 滚动高亮（`useScrollSpy`）+ 地址替换（`origin`）。
 *
 * 鉴权由路由守卫提供：`/docs` 未加 `meta.public`，未登录会被重定向到 `/login`。
 *
 * 内容目录命名为 `guide` 而非 `docs`：`.gitignore` 里的 `docs/` 规则会匹配任意层级，
 * 用 `docs` 会导致内容文件无法提交，克隆后构建直接失败。
 */
import { computed, ref } from 'vue'
import { Search } from '@element-plus/icons-vue'
import DocBlockList from '../components/DocBlockList.vue'
import { chapters } from '../guide'
import { collectSectionIds, filterChapters } from '../guide/useDocFilter'
import { useScrollSpy } from '../guide/useScrollSpy'

const keyword = ref('')
const useOrigin = ref(true)

/** 当前站点地址；SSR 或测试环境下 `window` 不存在时回退为空串。 */
const origin = computed(() => (typeof window === 'undefined' ? '' : window.location.origin))

/** 关键字过滤后的章节；关键字为空时即全量内容。 */
const visibleChapters = computed(() => filterChapters(chapters, keyword.value))

/** 参与滚动高亮的小节 id，顺序与文档一致。 */
const sectionIds = computed(() => collectSectionIds(visibleChapters.value))

const { activeId } = useScrollSpy(sectionIds)

/** 窄屏下拉导航的选项：带上章节名，避免只看到小节名而失去上下文。 */
const navItems = computed(() =>
  visibleChapters.value.flatMap((chapter) =>
    chapter.sections.map((section) => ({ id: section.id, label: `${chapter.title} · ${section.title}` }))
  )
)

/**
 * 跳转到指定小节并立即更新高亮。
 *
 * 先赋值 `activeId` 是为了让点击后立刻有反馈：`scrollIntoView` 在平滑滚动期间
 * 不会产生 IntersectionObserver 回调，否则高亮会滞后到滚动结束。
 * id 为空（过滤后章节没有小节）时直接返回，不做任何滚动。
 *
 * @param id 目标小节 id；可为 undefined
 */
function jumpTo(id?: string): void {
  if (!id) return
  document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  activeId.value = id
}
</script>

<style scoped>
.docs-page {
  min-width: 0;
  padding-bottom: 32px;
}
.page-sub {
  margin: 4px 0 0;
  color: var(--muted);
  font-size: 13px;
}
.docs-actions {
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 0;
}
.docs-search { width: 260px; }

.docs-layout {
  display: grid;
  grid-template-columns: 216px minmax(0, 1fr);
  gap: 20px;
  align-items: start;
}

.docs-nav {
  position: sticky;
  top: 20px;
  max-height: calc(100vh - 120px);
  overflow-y: auto;
  padding: 12px 12px 14px;
  overscroll-behavior: contain;
}
.docs-nav-select { display: none; width: 100%; }
.docs-nav-chapter + .docs-nav-chapter { margin-top: 6px; }
.docs-nav-chapter-title {
  display: block;
  width: 100%;
  padding: 7px 8px 3px;
  border: 0;
  background: transparent;
  text-align: left;
  font-size: 12px;
  font-weight: 800;
  letter-spacing: 0.01em;
  color: var(--text);
  cursor: pointer;
}
.docs-nav-chapter-title:hover { color: var(--primary-ink); }
.docs-nav-link {
  display: block;
  width: 100%;
  padding: 5px 8px 5px 14px;
  border: 0;
  border-left: 2px solid transparent;
  border-radius: 0 8px 8px 0;
  background: transparent;
  text-align: left;
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--muted);
  cursor: pointer;
  transition: color 0.14s, background 0.14s, border-color 0.14s;
}
.docs-nav-link:hover { color: var(--text); background: #f6f7f9; }
.docs-nav-link.on {
  color: var(--primary-ink);
  background: var(--primary-soft);
  border-left-color: var(--primary);
  font-weight: 700;
}

.docs-content { min-width: 0; }
.docs-chapter + .docs-chapter { margin-top: 26px; }
.docs-chapter-title {
  margin: 0 0 12px;
  padding-left: 2px;
  font-size: 19px;
  letter-spacing: -0.02em;
}
.docs-section {
  scroll-margin-top: 18px;
  padding: 16px 18px 18px;
}
.docs-section + .docs-section { margin-top: 14px; }
.docs-section-title {
  margin: 0 0 10px;
  padding-bottom: 9px;
  border-bottom: 1px solid var(--border);
  font-size: 15.5px;
  letter-spacing: -0.01em;
}

.docs-empty {
  padding: 28px 20px;
  text-align: center;
  font-size: 14px;
}
.docs-empty p { margin: 6px 0 0; font-size: 13px; }

@media (max-width: 1180px) {
  .docs-layout { grid-template-columns: 190px minmax(0, 1fr); gap: 16px; }
}

@media (max-width: 980px) {
  .docs-layout { grid-template-columns: minmax(0, 1fr); gap: 14px; }
  .docs-nav {
    position: static;
    max-height: none;
    padding: 10px;
  }
  .docs-nav-select { display: block; }
  .docs-nav-list { display: none; }
  .docs-search { width: 100%; }
  .docs-actions { width: 100%; }
}
</style>
