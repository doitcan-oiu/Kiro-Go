<script setup>
// Content-layer container (§3.3: content uses opaque surfaces, never glass).
// Header row holds the title plus optional actions.
defineProps({
  title: { type: String, default: '' },
  hint: { type: String, default: '' },
  /** Optional leading icon component (e.g. a Phosphor icon). */
  icon: { type: [Object, Function], default: null },
  /** Removes body padding for flush tables. */
  flush: { type: Boolean, default: false },
})
</script>

<template>
  <section class="surface-card overflow-hidden">
    <header
      v-if="title || $slots.title || $slots.actions"
      class="flex flex-wrap items-center justify-between gap-3 border-b border-divider px-lg py-md"
    >
      <div class="min-w-0">
        <h2 class="section-title flex items-center gap-sm">
          <component :is="icon" v-if="icon" :size="18" class="shrink-0 text-txt-tertiary" aria-hidden="true" />
          <slot name="title">{{ title }}</slot>
        </h2>
        <p v-if="hint || $slots.hint" class="mt-1 text-caption text-txt-tertiary">
          <slot name="hint">{{ hint }}</slot>
        </p>
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-sm">
        <slot name="actions" />
      </div>
    </header>
    <div :class="flush ? '' : 'p-lg'">
      <slot />
    </div>
  </section>
</template>
