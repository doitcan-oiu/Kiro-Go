<script setup>
// 应用外壳：整体是一个「悬浮盒子」——四边留白、大圆角、玻璃材质，浮在环境光
// 背景之上，而不是把内容铺满整个视口。
//
// 结构上刻意采用「固定高度盒子 + 内部滚动」而非「整页滚动 + sticky 顶栏」：
//
//   盒子需要 overflow: hidden 才能让内部的侧边栏描边、顶栏描边自动跟随外圆角
//   裁切。但 overflow: hidden 会使该元素成为滚动容器，于此之内的 `sticky top-0`
//   将相对这个「永不滚动」的盒子定位——顶栏因此完全失去吸附效果，同时超出的
//   内容被直接裁掉。两者不可兼得。
//
//   因此改为：外壳撑满视口高度（h-dvh），盒子填满外壳，顶栏与页脚 shrink-0
//   自然常驻，只有中间的 main 区域滚动。顶栏不需要 sticky，圆角裁切也成立。
//
// 响应式（§11.1）：
//   ≥1440px  280px 完整侧边栏
//   1024px+  64px 图标栏（用户可展开为 280px）
//   <1024px  抽屉式，从顶栏按钮唤出
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { PhList, PhSignOut } from '@phosphor-icons/vue'
import { navRoutes } from '@/router'
import { useI18n } from '@/lib/i18n'
import { useSessionStore } from '@/stores/session'
import { usePrefsStore } from '@/stores/prefs'
import { useDataStore } from '@/stores/data'
import BaseButton from '@/components/ui/BaseButton.vue'
import LangSwitch from '@/components/layout/LangSwitch.vue'
import ThemeToggle from '@/components/layout/ThemeToggle.vue'
import SidebarNav from '@/components/layout/SidebarNav.vue'
import BrandMark from '@/components/layout/BrandMark.vue'

const { t } = useI18n()
const route = useRoute()
const session = useSessionStore()
const prefs = usePrefsStore()
const data = useDataStore()

const isNarrow = ref(false)
const isLaptop = ref(false)
const drawerOpen = ref(false)

let mqNarrow
let mqLaptop

function syncQueries() {
  isNarrow.value = mqNarrow.matches
  isLaptop.value = mqLaptop.matches
}

onMounted(() => {
  mqNarrow = window.matchMedia('(max-width: 1023px)')
  mqLaptop = window.matchMedia('(min-width: 1024px) and (max-width: 1439px)')
  syncQueries()
  mqNarrow.addEventListener('change', syncQueries)
  mqLaptop.addEventListener('change', syncQueries)
})

onBeforeUnmount(() => {
  mqNarrow?.removeEventListener('change', syncQueries)
  mqLaptop?.removeEventListener('change', syncQueries)
})

// 视口变宽后抽屉必须收起，否则它会以静态侧边栏之外的第二块面板残留在屏幕上。
watch(isNarrow, (narrow) => {
  if (!narrow) drawerOpen.value = false
})

// 切换路由时收起抽屉——移动端点完菜单项就该看到内容。
watch(
  () => route.fullPath,
  () => {
    drawerOpen.value = false
  },
)

const railMode = computed(() => !isNarrow.value && (prefs.sidebarCollapsed || isLaptop.value))

const pageTitle = computed(() => (route.meta?.titleKey ? t(route.meta.titleKey) : ''))

// 内容区滚动容器：路由切换后回到顶部。整页滚动时浏览器会自动处理，
// 改成内部滚动后需要自己复位，否则从长列表跳到短页面会停在中途。
const scroller = ref(null)
watch(
  () => route.fullPath,
  () => {
    scroller.value?.scrollTo({ top: 0 })
  },
)

function onEscape(event) {
  if (event.key === 'Escape') drawerOpen.value = false
}
</script>

<template>
  <!-- 外层只负责视口尺寸与四周留白，留白宽度随断点递增。 -->
  <div class="h-dvh p-3 text-txt sm:p-5 laptop:p-6 desktop:p-8">
    <a
      href="#main-content"
      class="sr-only-focusable btn btn-primary btn-sm fixed top-3 left-3 z-[var(--z-toast)]"
    >
      {{ t('a11y.skipToContent') }}
    </a>

    <!-- 盒子本体：玻璃材质 + 大圆角 + 裁切。内部各栏的描边因此自动贴合外圆角。 -->
    <div
      class="glass-thin relative flex h-full overflow-hidden rounded-[20px] shadow-[var(--sh-lg)]"
    >
      <!-- ═══ 侧边栏：静态面板（≥1024px）═══ -->
      <aside
        v-if="!isNarrow"
        class="flex shrink-0 flex-col border-r border-line bg-[var(--surface-rail)] transition-[width] duration-[var(--dur-normal)] ease-[var(--ease-out-expo)]"
        :style="{ width: railMode ? 'var(--sidebar-w-collapsed)' : 'var(--sidebar-w)' }"
      >
        <SidebarNav :rail="railMode" :routes="navRoutes" @toggle="prefs.toggleSidebar()" />
      </aside>

      <!-- ═══ 侧边栏：抽屉（<1024px）═══
           absolute 而非 fixed：遮罩与抽屉都限制在盒子内，不会盖住四周留白。 -->
      <Transition name="fade">
        <div
          v-if="isNarrow && drawerOpen"
          class="absolute inset-0 z-[var(--z-drawer)] bg-black/55 backdrop-blur-[2px]"
          @click="drawerOpen = false"
        />
      </Transition>

      <Transition name="drawer">
        <aside
          v-if="isNarrow && drawerOpen"
          class="glass-thick absolute inset-y-0 left-0 z-[var(--z-drawer)] flex w-[min(var(--sidebar-w),82vw)] flex-col shadow-[var(--sh-lg)]"
          role="dialog"
          :aria-label="t('aria.sections')"
          @keydown="onEscape"
        >
          <SidebarNav :rail="false" :routes="navRoutes" show-close @close="drawerOpen = false" />
        </aside>
      </Transition>

      <!-- ═══ 主列：顶栏常驻 / 内容滚动 / 页脚常驻 ═══
           min-w-0 让长内容（表格、代码块）触发自身的截断而不是把整列撑宽。 -->
      <div class="flex min-w-0 flex-1 flex-col">
        <header
          class="flex h-[var(--topbar-h)] shrink-0 items-center gap-md border-b border-line bg-[var(--surface-topbar)] px-md backdrop-blur-[20px] laptop:px-lg"
        >
          <button
            v-if="isNarrow"
            type="button"
            class="btn btn-ghost btn-icon -ml-1.5"
            :aria-label="t('aria.sections')"
            :aria-expanded="drawerOpen"
            @click="drawerOpen = true"
          >
            <PhList :size="20" />
          </button>

          <h1 class="truncate font-brand text-title-md">{{ pageTitle }}</h1>

          <div class="ml-auto flex items-center gap-sm">
            <span
              class="hidden items-center gap-2 text-caption text-txt-tertiary laptop:flex"
              :title="t('status.running')"
            >
              <span class="status-dot status-dot-green" aria-hidden="true" />
              {{ t('status.running') }}
            </span>
            <span class="hidden h-5 w-px bg-line laptop:block" aria-hidden="true" />
            <LangSwitch />
            <ThemeToggle />
            <BaseButton
              variant="ghost"
              size="sm"
              icon
              :title="t('common.logout')"
              :aria-label="t('common.logout')"
              @click="session.logout()"
            >
              <template #icon><PhSignOut :size="18" /></template>
            </BaseButton>
          </div>
        </header>

        <!-- 唯一的滚动区。min-h-0 是必需的：flex 子项默认 min-height:auto，
             不归零则它会被内容撑高而不产生滚动条。 -->
        <main
          id="main-content"
          ref="scroller"
          class="min-h-0 min-w-0 flex-1 overflow-y-auto overscroll-contain p-md laptop:p-lg"
        >
          <RouterView v-slot="{ Component }">
            <Transition name="page" mode="out-in">
              <component :is="Component" />
            </Transition>
          </RouterView>
        </main>

        <footer
          class="flex shrink-0 flex-wrap items-center gap-md border-t border-line px-md py-2.5 text-caption text-txt-tertiary laptop:px-lg"
        >
          <span class="flex items-center gap-2">
            <BrandMark :size="15" />
            Kiro-Go
          </span>
          <span v-if="data.version" :aria-label="t('aria.currentVersion')">
            v{{ data.version.replace(/^v/, '') }}
          </span>
          <a
            class="ml-auto inline-flex items-center gap-2 transition-colors hover:text-txt"
            href="https://github.com/Quorinex/Kiro-Go"
            target="_blank"
            rel="noopener noreferrer"
          >
            {{ t('footer.github') }}
          </a>
          <span>© {{ new Date().getFullYear() }}</span>
        </footer>
      </div>
    </div>
  </div>
</template>

<style scoped>
.drawer-enter-active,
.drawer-leave-active {
  transition: transform var(--dur-normal) var(--ease-out-expo);
}
.drawer-enter-from,
.drawer-leave-to {
  transform: translateX(-100%);
}
</style>
