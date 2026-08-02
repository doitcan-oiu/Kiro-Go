<script setup>
// Sidebar contents per §9.1: brand at top, 44px nav rows in the middle,
// user/status block pinned to the bottom.
import { computed } from 'vue'
import {
  PhCaretLeft,
  PhCaretRight,
  PhGauge,
  PhKey,
  PhListDashes,
  PhPlug,
  PhShoppingCartSimple,
  PhSlidersHorizontal,
  PhUsers,
  PhX,
} from '@phosphor-icons/vue'
import { useI18n } from '@/lib/i18n'
import { useDataStore } from '@/stores/data'
import BrandMark from '@/components/layout/BrandMark.vue'

const props = defineProps({
  routes: { type: Array, required: true },
  rail: { type: Boolean, default: false },
  showClose: { type: Boolean, default: false },
})

defineEmits(['toggle', 'close'])

const { t } = useI18n()
const data = useDataStore()

const ICONS = {
  gauge: PhGauge,
  users: PhUsers,
  key: PhKey,
  cart: PhShoppingCartSimple,
  list: PhListDashes,
  plug: PhPlug,
  sliders: PhSlidersHorizontal,
}

/**
 * 侧边栏标签取 meta.navKey，缺失时回退 meta.titleKey。
 *
 * 分成两个键是为了让侧边栏的中文标签统一成 4 个字（竖排对齐更整齐），而顶栏
 * 标题仍与页面正文措辞一致。回退保证新增路由忘了写 navKey 时只是措辞不齐，
 * 而不是显示成空白。
 */
function navLabel(item) {
  return t(item.meta?.navKey || item.meta?.titleKey || '')
}

// Two groups so the rail keeps a sensible reading order (§9.1 分组标题).
const groups = computed(() => [
  {
    key: 'nav.groupOperations',
    items: props.routes.filter((r) => ['dashboard', 'accounts', 'keys', 'replenish'].includes(r.name)),
  },
  {
    key: 'nav.groupSystem',
    items: props.routes.filter((r) => ['logs', 'api', 'settings'].includes(r.name)),
  },
])
</script>

<template>
  <!-- Brand -->
  <div class="flex h-[var(--topbar-h)] shrink-0 items-center gap-3 border-b border-line px-md">
    <BrandMark :size="28" class="shrink-0" />
    <span v-if="!rail" class="truncate font-brand text-[18px] font-bold tracking-tight">Kiro-Go</span>
    <button
      v-if="showClose"
      type="button"
      class="btn btn-ghost btn-icon ml-auto"
      :aria-label="t('common.close')"
      @click="$emit('close')"
    >
      <PhX :size="18" />
    </button>
  </div>

  <!-- Nav -->
  <nav class="min-h-0 flex-1 overflow-y-auto px-sm py-md" :aria-label="t('aria.sections')">
    <template v-for="group in groups" :key="group.key">
      <p v-if="!rail" class="nav-group-label px-2">{{ t(group.key) }}</p>
      <div v-else class="my-md h-px bg-line" aria-hidden="true" />

      <ul class="flex flex-col gap-1" role="list">
        <li v-for="item in group.items" :key="item.name">
          <RouterLink
            v-slot="{ isActive, href, navigate }"
            :to="item.path"
            custom
          >
            <a
              :href="href"
              :class="[
                'group relative flex h-11 items-center gap-3 rounded-[10px] px-3 text-body font-medium transition-colors duration-[var(--dur-fast)]',
                isActive
                  ? 'glass-thick text-accent'
                  : 'text-txt-secondary hover:bg-surface-hover hover:text-txt',
                rail && 'justify-center px-0',
              ]"
              :aria-current="isActive ? 'page' : undefined"
              :title="rail ? navLabel(item) : undefined"
              @click="navigate"
            >
              <!-- §9.1 选中态左侧 3px 强调色竖条 -->
              <span
                v-if="isActive"
                class="absolute top-1/2 left-0 h-5 w-[3px] -translate-y-1/2 rounded-r-full bg-accent"
                aria-hidden="true"
              />
              <component :is="ICONS[item.meta.icon]" :size="rail ? 22 : 20" class="shrink-0" />
              <span v-if="!rail" class="truncate">{{ navLabel(item) }}</span>
              <span
                v-if="!rail && item.name === 'accounts' && data.totalAccounts"
                class="tnum ml-auto text-caption-sm text-txt-tertiary"
              >
                {{ data.totalAccounts }}
              </span>
            </a>
          </RouterLink>
        </li>
      </ul>
    </template>
  </nav>

  <!-- Bottom: pool health + rail toggle -->
  <div class="shrink-0 border-t border-line p-sm">
    <div
      v-if="!rail"
      class="mb-sm flex items-center gap-3 rounded-[10px] px-3 py-2"
    >
      <span
        class="status-dot"
        :class="data.availableAccounts > 0 ? 'status-dot-green' : 'status-dot-red'"
        aria-hidden="true"
      />
      <div class="min-w-0 flex-1">
        <p class="truncate text-caption text-txt-secondary">{{ t('stats.capacity') }}</p>
        <p class="tnum truncate text-caption-sm text-txt-tertiary">
          {{ data.availableAccounts }} / {{ data.totalAccounts }}
        </p>
      </div>
    </div>

    <button
      type="button"
      class="flex h-9 w-full items-center justify-center gap-2 rounded-[10px] text-caption text-txt-tertiary transition-colors hover:bg-surface-hover hover:text-txt"
      :aria-label="t(rail ? 'nav.expandSidebar' : 'nav.collapseSidebar')"
      :title="t(rail ? 'nav.expandSidebar' : 'nav.collapseSidebar')"
      @click="$emit('toggle')"
    >
      <PhCaretRight v-if="rail" :size="16" />
      <template v-else>
        <PhCaretLeft :size="16" />
        <span>{{ t('nav.collapseSidebar') }}</span>
      </template>
    </button>
  </div>
</template>
