<template>
  <div class="picker">
    <button
      type="button"
      class="input pbtn"
      :class="{ off: disabled }"
      :disabled="disabled"
      @click="open"
    >
      <span v-if="modelValue" class="mono val">{{ modelValue }}</span>
      <span v-else class="ph">{{ disabled ? '随机模式已开启' : '请选择账号' }}</span>
      <svg class="chev" viewBox="0 0 10 10"><path d="M2 3.6 5 6.6l3-3" /></svg>
    </button>

    <Teleport to="body">
      <div v-if="show" class="mask" @mousedown.self="close">
        <div class="dlg" @keydown.esc.stop="close">
          <div class="dlg-head">
            <input
              ref="searchEl"
              v-model="kw"
              class="input"
              placeholder="搜索账号 / 昵称"
              @keydown.enter="pickFirst"
            />
          </div>

          <div class="list">
            <div
              v-for="a in filtered"
              :key="a.account"
              class="row"
              :class="{ on: a.account === modelValue }"
              @click="pick(a)"
            >
              <span class="mono acc">{{ a.account }}</span>
              <span class="meta">{{ metaOf(a) }}</span>
            </div>
            <div v-if="!filtered.length" class="empty">没有匹配的账号</div>
          </div>

          <div class="dlg-foot">{{ filtered.length }} / {{ accounts.length }}</div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup>
import { ref, computed, nextTick, watch } from 'vue'

const props = defineProps({
  modelValue: { type: String, default: '' },
  accounts: { type: Array, default: () => [] },
  disabled: { type: Boolean, default: false }
})
const emit = defineEmits(['update:modelValue'])

const show = ref(false)
const kw = ref('')
const searchEl = ref(null)

const filtered = computed(() => {
  const q = kw.value.trim().toLowerCase()
  if (!q) return props.accounts
  return props.accounts.filter((a) => {
    const s = [a.account, a.nick].filter(Boolean).join(' ').toLowerCase()
    return s.includes(q)
  })
})

function metaOf(a) {
  return a.nick || ''
}

async function open() {
  if (props.disabled) return
  kw.value = ''
  show.value = true
  await nextTick()
  searchEl.value?.focus()
}

function close() {
  show.value = false
}

function pick(a) {
  emit('update:modelValue', a.account)
  close()
}

function pickFirst() {
  if (filtered.value.length) pick(filtered.value[0])
}

// 外部把值清掉（比如勾回随机模式）时把弹窗也关掉
watch(
  () => props.disabled,
  (v) => {
    if (v) close()
  }
)
</script>

<style scoped>
.picker {
  min-width: 0;
}

.pbtn {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  text-align: left;
  padding-right: 8px;
}

.pbtn.off {
  background: #fafbfc;
  color: var(--text-3);
}

.val {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ph {
  flex: 1;
  color: var(--text-3);
}

.chev {
  width: 10px;
  height: 10px;
  flex: none;
  fill: none;
  stroke: currentColor;
  stroke-width: 1.2;
  stroke-linecap: round;
  stroke-linejoin: round;
  color: var(--text-3);
}

.mask {
  position: fixed;
  inset: 0;
  background: rgba(20, 26, 38, 0.28);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding-top: 96px;
  z-index: 60;
}

.dlg {
  width: 380px;
  max-width: calc(100vw - 48px);
  background: var(--panel);
  border-radius: 10px;
  box-shadow: 0 18px 48px rgba(20, 26, 38, 0.22);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.dlg-head {
  padding: 12px;
  border-bottom: 1px solid var(--line);
}

.list {
  max-height: 300px;
  overflow: auto;
  padding: 4px 0;
}

.row {
  display: flex;
  align-items: baseline;
  gap: 10px;
  padding: 7px 14px;
  cursor: default;
}

.row:hover {
  background: #f5f7fa;
}

.row.on {
  background: var(--blue-soft);
}

.acc {
  font-size: 12.5px;
  flex: none;
}

.meta {
  font-size: 12px;
  color: var(--text-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.dlg-foot {
  padding: 8px 14px;
  border-top: 1px solid var(--line);
  font-size: 11.5px;
  color: var(--text-3);
}
</style>
