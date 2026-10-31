<template>
  <div v-if="!maximized" class="rz">
    <div
      v-for="z in zones"
      :key="z.edge"
      class="rz-z"
      :class="'rz-' + z.edge"
      :style="{ cursor: z.cursor }"
      @mousedown.prevent="start(z.edge)"
    ></div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { api } from '../api'

const maximized = ref(false)

const zones = [
  { edge: 'top', cursor: 'ns-resize' },
  { edge: 'bottom', cursor: 'ns-resize' },
  { edge: 'left', cursor: 'ew-resize' },
  { edge: 'right', cursor: 'ew-resize' },
  { edge: 'topleft', cursor: 'nwse-resize' },
  { edge: 'topright', cursor: 'nesw-resize' },
  { edge: 'bottomleft', cursor: 'nesw-resize' },
  { edge: 'bottomright', cursor: 'nwse-resize' }
]

function start(edge) {
  if (maximized.value) return
  api.winResize(edge).catch(() => {})
}

async function sync() {
  try {
    const r = await api.winState()
    maximized.value = !!r.maximized
  } catch (e) {
    /* 忽略 */
  }
}

onMounted(() => {
  sync()
  window.addEventListener('resize', sync)
})

onBeforeUnmount(() => window.removeEventListener('resize', sync))
</script>

<style scoped>
.rz {
  position: fixed;
  inset: 0;
  z-index: 9999;
  pointer-events: none;
}

.rz-z {
  position: absolute;
  pointer-events: auto;
  user-select: none;
  -webkit-user-select: none;
}

.rz-top,
.rz-bottom {
  left: 8px;
  right: 8px;
  height: 5px;
}

.rz-left,
.rz-right {
  top: 8px;
  bottom: 8px;
  width: 5px;
}

.rz-top {
  top: 0;
}
.rz-bottom {
  bottom: 0;
}
.rz-left {
  left: 0;
}
.rz-right {
  right: 0;
}

.rz-topleft,
.rz-topright,
.rz-bottomleft,
.rz-bottomright {
  width: 8px;
  height: 8px;
}

.rz-topleft {
  top: 0;
  left: 0;
}
.rz-topright {
  top: 0;
  right: 0;
}
.rz-bottomleft {
  bottom: 0;
  left: 0;
}
.rz-bottomright {
  bottom: 0;
  right: 0;
}
</style>
