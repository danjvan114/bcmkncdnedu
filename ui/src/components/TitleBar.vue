<template>
  <div class="titlebar">
    <div class="drag" @mousedown.prevent="startDrag" @dblclick.prevent="toggleMax">
      <span class="name">Edu</span>
    </div>
    <div class="acts">
      <button class="wbtn" title="最小化" @click.stop="api.winMin().catch(() => {})">
        <svg viewBox="0 0 10 10"><path d="M1.5 5h7" /></svg>
      </button>
      <button class="wbtn" :title="maximized ? '还原' : '最大化'" @click.stop="toggleMax">
        <svg v-if="!maximized" viewBox="0 0 10 10">
          <rect x="1.4" y="1.4" width="7.2" height="7.2" rx="1" />
        </svg>
        <svg v-else viewBox="0 0 10 10">
          <rect x="1.2" y="3.2" width="5.6" height="5.6" rx="1" />
          <path d="M3.6 3.2V1.9a.7.7 0 0 1 .7-.7h3.8a.7.7 0 0 1 .7.7v3.8a.7.7 0 0 1-.7.7H6.8" />
        </svg>
      </button>
      <button class="wbtn close" title="关闭" @click.stop="api.winClose().catch(() => {})">
        <svg viewBox="0 0 10 10"><path d="M1.8 1.8l6.4 6.4M8.2 1.8l-6.4 6.4" /></svg>
      </button>
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { api } from '../api'

const maximized = ref(false)
let timer = null

function startDrag(e) {
  if (e.button !== 0) return
  api.winDrag().catch(() => {})
}

async function toggleMax() {
  try {
    const r = await api.winMax()
    maximized.value = !!r.maximized
  } catch (e) {
    /* 忽略 */
  }
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
  timer = setInterval(sync, 1500)
  window.addEventListener('resize', sync)
})

onBeforeUnmount(() => {
  if (timer) clearInterval(timer)
  window.removeEventListener('resize', sync)
})
</script>

<style scoped>
.titlebar {
  flex: none;
  height: 38px;
  display: flex;
  align-items: stretch;
  background: #fff;
  border-bottom: 1px solid var(--line);
}

.drag {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  padding-left: 14px;
}

.name {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text);
  pointer-events: none;
}

.acts {
  display: flex;
  flex: none;
}

.wbtn {
  width: 44px;
  height: 38px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: none;
  background: transparent;
  color: var(--text-2);
}

.wbtn svg {
  width: 10px;
  height: 10px;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.1;
  stroke-linecap: round;
}

.wbtn:hover {
  background: #f2f4f7;
  color: var(--text);
}

.wbtn.close:hover {
  background: #e81123;
  color: #fff;
}
</style>
