// Router.
//
// History base is `/admin/` because the Go server mounts the panel there and
// serves index.html for any unmatched `/admin/*` path (SPA fallback).
//
// meta 上有两个标签键，刻意分开：
//   titleKey — 顶栏标题，沿用与页面正文一致的措辞
//   navKey   — 侧边栏标签，中文统一 4 字以便竖排对齐
// 合成一个键会迫使两处共享措辞：把 apiKeys.listTitle 改成「密钥管理」会连带
// 改掉 ApiKeysView 里那张卡片的标题。
import { createRouter, createWebHistory } from 'vue-router'

const routes = [
  {
    path: '/',
    name: 'dashboard',
    component: () => import('@/views/DashboardView.vue'),
    meta: { titleKey: 'nav.dashboard', navKey: 'nav.dashboard', icon: 'gauge' },
  },
  {
    path: '/accounts',
    name: 'accounts',
    component: () => import('@/views/AccountsView.vue'),
    meta: { titleKey: 'tabs.accounts', navKey: 'nav.accounts', icon: 'users' },
  },
  {
    path: '/keys',
    name: 'keys',
    component: () => import('@/views/ApiKeysView.vue'),
    meta: { titleKey: 'apiKeys.listTitle', navKey: 'nav.keys', icon: 'key' },
  },
  {
    path: '/replenish',
    name: 'replenish',
    component: () => import('@/views/ReplenishView.vue'),
    meta: { titleKey: 'tabs.replenish', navKey: 'nav.replenish', icon: 'cart' },
  },
  {
    path: '/logs',
    name: 'logs',
    component: () => import('@/views/LogsView.vue'),
    meta: { titleKey: 'tabs.logs', navKey: 'nav.logs', icon: 'list' },
  },
  {
    path: '/api',
    name: 'api',
    component: () => import('@/views/ApiView.vue'),
    meta: { titleKey: 'tabs.api', navKey: 'nav.api', icon: 'plug' },
  },
  {
    path: '/settings',
    name: 'settings',
    component: () => import('@/views/SettingsView.vue'),
    meta: { titleKey: 'tabs.settings', navKey: 'nav.settings', icon: 'sliders' },
  },
  // Unknown paths land on the dashboard rather than a dead end.
  { path: '/:pathMatch(.*)*', redirect: '/' },
]

export const router = createRouter({
  history: createWebHistory('/admin/'),
  routes,
  scrollBehavior: () => ({ top: 0 }),
})

/** Nav order for the sidebar; excludes the catch-all. */
export const navRoutes = routes.filter((r) => r.name)

export default router
