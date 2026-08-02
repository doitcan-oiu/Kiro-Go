<script setup>
// 可访问的标签页（WAI-ARIA Tabs 模式）。
//
// 为什么不用「一堆按钮 + v-if」凑：标签页有一套明确的键盘契约，手写按钮往往
// 只做对了点击。这里实现完整的 roving tabindex：
//   - 只有当前选中的 tab 可被 Tab 键聚焦（tabindex=0），其余为 -1，
//     于是 Tab 键在整组标签上只停一次，而不是逐个穿过 N 个供应商；
//   - ←/→ 在标签间移动并切换，Home/End 跳到首/末，均带环绕。
//
// 面板内容由调用方通过默认插槽渲染（只渲染当前项），因此切换标签不会同时挂载
// 全部供应商的表单——供应商数量增长时这一点直接决定首屏成本。
import { computed, ref, useId, watch } from 'vue'

const props = defineProps({
  /** `{ value, label, badge?, tone? }`；value 为唯一标识。 */
  items: { type: Array, default: () => [] },
  modelValue: { type: [String, Number], default: '' },
  ariaLabel: { type: String, default: '' },
})

const emit = defineEmits(['update:modelValue'])

const uid = useId()
const tabRefs = ref([])

const values = computed(() => props.items.map((i) => i.value))

const activeIndex = computed(() => {
  const i = values.value.indexOf(props.modelValue)
  // 选中项被删除（例如某家供应商被移除）时回落到第一项，避免面板空白。
  return i >= 0 ? i : 0
})

const tabId = (v) => `${uid}-tab-${v}`
const panelId = (v) => `${uid}-panel-${v}`

function select(index) {
  const item = props.items[index]
  if (!item || item.value === props.modelValue) return
  emit('update:modelValue', item.value)
}

/** 移动焦点并切换。ARIA 的自动激活模式：方向键移动即选中。 */
function move(delta) {
  const n = props.items.length
  if (!n) return
  const next = (activeIndex.value + delta + n) % n
  select(next)
  focusTab(next)
}

function focusTab(index) {
  // 等 DOM 更新后再聚焦：切换会重渲染 tabindex，过早聚焦会被覆盖。
  requestAnimationFrame(() => tabRefs.value[index]?.focus())
}

function onKeydown(event) {
  switch (event.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      event.preventDefault()
      move(1)
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      event.preventDefault()
      move(-1)
      break
    case 'Home':
      event.preventDefault()
      select(0)
      focusTab(0)
      break
    case 'End':
      event.preventDefault()
      select(props.items.length - 1)
      focusTab(props.items.length - 1)
      break
  }
}

// 选中项消失时同步回落，保持 modelValue 与实际渲染一致。
watch(
  () => [props.items.length, props.modelValue],
  () => {
    if (props.items.length && !values.value.includes(props.modelValue)) {
      emit('update:modelValue', props.items[0].value)
    }
  },
)

const activeItem = computed(() => props.items[activeIndex.value] || null)
</script>

<template>
  <div>
    <!-- 标签条：玻璃胶囊内嵌一排，选中项浮起为 thick 材质（§3.3 允许的
         「选中态浮起」场景） -->
    <div
      class="glass-clear flex gap-1 overflow-x-auto rounded-full p-1"
      role="tablist"
      :aria-label="ariaLabel || undefined"
      @keydown="onKeydown"
    >
      <button
        v-for="(item, i) in items"
        :key="item.value"
        :ref="(el) => (tabRefs[i] = el)"
        type="button"
        role="tab"
        :id="tabId(item.value)"
        :aria-selected="i === activeIndex"
        :aria-controls="panelId(item.value)"
        :tabindex="i === activeIndex ? 0 : -1"
        :class="[
          'flex shrink-0 items-center gap-2 rounded-full px-3.5 py-1.5 text-body-sm font-medium whitespace-nowrap transition-colors duration-[var(--dur-fast)]',
          i === activeIndex
            ? 'glass-thick text-txt'
            : 'text-txt-tertiary hover:bg-surface-hover hover:text-txt-secondary',
        ]"
        @click="select(i)"
      >
        <span
          v-if="item.tone"
          class="status-dot h-1.5! w-1.5!"
          :class="`status-dot-${item.tone}`"
          aria-hidden="true"
        />
        <span>{{ item.label }}</span>
        <span
          v-if="item.badge"
          class="tnum text-caption-sm text-txt-tertiary"
          aria-hidden="true"
        >
          {{ item.badge }}
        </span>
      </button>
    </div>

    <!-- 只渲染当前面板：切换标签不会同时挂载全部供应商表单 -->
    <div
      v-if="activeItem"
      :id="panelId(activeItem.value)"
      role="tabpanel"
      :aria-labelledby="tabId(activeItem.value)"
      tabindex="0"
      class="mt-md outline-none"
    >
      <slot :item="activeItem" :index="activeIndex" />
    </div>
  </div>
</template>
