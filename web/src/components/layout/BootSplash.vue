<script setup>
// Shown while the stored credential is probed against GET /status. Keeping the
// paint blank-but-branded avoids a login-screen flash for returning users.
import { PhSpinnerGap } from '@phosphor-icons/vue'
</script>

<template>
  <div class="flex min-h-dvh items-center justify-center bg-bg">
    <div class="flex flex-col items-center gap-md">
      <span class="font-brand text-title-md tracking-tight text-txt">
        Kiro<span class="text-accent">-Go</span>
      </span>
      <PhSpinnerGap :size="22" class="anim-spin text-txt-tertiary" aria-hidden="true" />
      <span class="sr-only">Loading</span>
    </div>
  </div>
</template>
