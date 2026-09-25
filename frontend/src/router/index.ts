import { createRouter, createWebHistory } from 'vue-router'
import { useSessionStore } from '../stores/session'

const LoginView = () => import('../views/LoginView.vue')
const DashboardView = () => import('../views/DashboardView.vue')
const ProvidersView = () => import('../views/ProvidersView.vue')
const TokensView = () => import('../views/TokensView.vue')
const PlaygroundView = () => import('../views/PlaygroundView.vue')
const FetchView = () => import('../views/FetchView.vue')
const LogsView = () => import('../views/LogsView.vue')
const AuditLogsView = () => import('../views/AuditLogsView.vue')
const SettingsView = () => import('../views/SettingsView.vue')
const DocsView = () => import('../views/DocsView.vue')

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', component: LoginView, meta: { public: true } },
    { path: '/', component: DashboardView },
    { path: '/providers', component: ProvidersView },
    { path: '/keys', redirect: '/providers' },
    { path: '/tokens', component: TokensView },
    { path: '/playground', component: PlaygroundView },
    // 刻意不设 meta.public：抓取配置含代理与内网放行开关，必须与其余功能页一样先登录
    { path: '/fetch', component: FetchView },
    { path: '/logs', component: LogsView },
    { path: '/audit', component: AuditLogsView },
    { path: '/usage', redirect: '/' },
    { path: '/settings', component: SettingsView },
    // 刻意不设 meta.public：使用文档会把渠道参数、配错后果等内部细节摊开，
    // 必须与其余功能页一样先登录才能查看。
    { path: '/docs', component: DocsView }
  ]
})

router.beforeEach((to) => {
  const session = useSessionStore()
  if (!to.meta.public && !session.token) return '/login'
  if (to.path === '/login' && session.token) return '/playground'
})

export default router
