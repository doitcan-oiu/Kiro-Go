<script setup>
// Brand mark: a stacked-layer glyph echoing §1.2 ("3D 堆叠卡片 = 合同层级").
// Inline SVG rather than the 104 KB favicon.ico, so it stays crisp at every
// size, inherits the accent colour, and costs no extra request.
defineProps({
  size: { type: [Number, String], default: 28 },
})
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 32 32"
    fill="none"
    aria-hidden="true"
    class="shrink-0"
  >
    <rect x="1" y="1" width="30" height="30" rx="9" fill="var(--accent-primary)" opacity="0.14" />
    <rect
      x="1.5"
      y="1.5"
      width="29"
      height="29"
      rx="8.5"
      stroke="var(--accent-primary)"
      stroke-opacity="0.32"
    />
    <!-- Three offset layers, back to front, opacity increasing (§6.3). -->
    <rect x="9" y="20" width="14" height="3.5" rx="1.75" fill="var(--accent-primary)" opacity="0.35" />
    <rect x="8" y="14.5" width="16" height="3.5" rx="1.75" fill="var(--accent-primary)" opacity="0.6" />
    <rect x="7" y="9" width="18" height="3.5" rx="1.75" fill="var(--accent-primary)" />
  </svg>
</template>
