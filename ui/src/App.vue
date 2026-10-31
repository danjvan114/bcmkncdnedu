<template>
  <div class="app">
    <TitleBar />

    <div class="shell">
      <aside class="side">
        <div class="brand">
          <div class="brand-name">Edu</div>
        </div>

        <div
          v-for="it in navs"
          :key="it.key"
          class="nav"
          :class="{ active: page === it.key }"
          @click="page = it.key"
        >
          <span v-html="it.icon"></span>
          <span>{{ it.label }}</span>
        </div>

        <div class="side-foot">账号 {{ stats.total }} · 可用 {{ stats.ok }}</div>
      </aside>

      <main class="main">
        <HomeView v-if="page === 'home'" />
        <UploadView v-else-if="page === 'upload'" />
        <SettingsView v-else />
      </main>
    </div>

    <ResizeFrame />

    <div v-if="toast" class="toast" :class="toast.kind">{{ toast.text }}</div>
  </div>
</template>

<script setup>
import { ref, reactive, provide, onMounted, onBeforeUnmount, computed } from 'vue'
import TitleBar from './components/TitleBar.vue'
import ResizeFrame from './components/ResizeFrame.vue'
import HomeView from './views/HomeView.vue'
import UploadView from './views/UploadView.vue'
import SettingsView from './views/SettingsView.vue'
import { api } from './api'

const page = ref('home')

const navs = [
  {
    key: 'home',
    label: '账号管理',
    icon: '<svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="3.4"/><path d="M4.8 19.4c.6-3.4 3.6-5.4 7.2-5.4s6.6 2 7.2 5.4"/></svg>'
  },
  {
    key: 'upload',
    label: '上传视频',
    icon: '<svg viewBox="0 0 24 24"><path d="M12 16V5"/><path d="M7.5 9.5 12 5l4.5 4.5"/><path d="M4.5 15v3.5a1 1 0 0 0 1 1h13a1 1 0 0 0 1-1V15"/></svg>'
  },
  {
    key: 'settings',
    label: '设置',
    icon: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M12 3v2.2M12 18.8V21M3 12h2.2M18.8 12H21M5.6 5.6l1.6 1.6M16.8 16.8l1.6 1.6M18.4 5.6l-1.6 1.6M7.2 16.8l-1.6 1.6"/></svg>'
  }
]

// ---- 全局状态（每 700ms 拉一次，页面直接读） ----
const state = reactive({
  accounts: [],
  stats: { total: 0, ok: 0, fail: 0, pending: 0 },
  verify: { busy: false, done: 0, total: 0 },
  settings: { m3u8Dir: '' },
  ffmpeg: '',
  job: null,
  busy: false
})

const stats = computed(() => state.stats)

provide('state', state)
provide('reload', load)

const toast = ref(null)
let toastTimer = null
function notify(text, kind = 'ok') {
  toast.value = { text, kind }
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (toast.value = null), 2600)
}
provide('notify', notify)

async function load() {
  try {
    const r = await api.state()
    state.accounts = r.accounts || []
    state.stats = r.stats || { total: 0, ok: 0, fail: 0, pending: 0 }
    state.verify = r.verify || { busy: false, done: 0, total: 0 }
    state.settings = r.settings || { m3u8Dir: '' }
    state.ffmpeg = r.ffmpeg || ''
    state.job = r.job || null
    state.busy = !!r.busy
  } catch (e) {
    /* 本地服务还没起来时忽略 */
  }
}

let timer = null
onMounted(() => {
  load()
  timer = setInterval(load, 700)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<style scoped>
.toast {
  position: fixed;
  left: 50%;
  bottom: 26px;
  transform: translateX(-50%);
  max-width: 70%;
  padding: 9px 16px;
  border-radius: 8px;
  font-size: 12.5px;
  color: #fff;
  background: #2b3240;
  box-shadow: 0 8px 24px rgba(20, 26, 38, 0.18);
  z-index: 50;
}

.toast.bad {
  background: #d1373c;
}

.toast.warn {
  background: #b46a08;
}
</style>
