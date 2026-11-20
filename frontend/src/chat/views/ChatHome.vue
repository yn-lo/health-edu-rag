<script setup lang="ts">
/**
 * ChatHome 聊天首页 - AI Healthcare Native 综合风格
 *
 * 融合 4 种风格特长：
 * - AI-Native UI -> 浅灰底 #F5F5F5 / clean input / context cards border-left
 * - Hero-Centric -> 96px hero orb + 双层 pulse 光晕 + 渐变头像
 * - Micro-interactions-> 100ms hover scale 1.02 / press scale 0.97 / 输入卡片 focus 增强
 * - Accessible & Ethical -> 3px focus ring / 44px 触摸目标 / reduced-motion
 * 保留功能：科室选择、历史抽屉、推荐问题、SSE 触发
 */
import { computed, defineAsyncComponent, ref, watch, type Component } from 'vue'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import {
 Activity,
 BookOpen,
 ChevronRight,
 Droplet,
 HeartPulse,
 Sparkles,
} from '@lucide/vue'
import { useDsToast } from '@/shared/composables/useDsToast'
import { wikiApi } from '@/shared'
import { useDepartments } from '@/chat/composables/useDepartments'
import { useChatMode } from '@/chat/composables/useChatMode'
import { DisclaimerFooter } from '@/shared/components'
import ChatHeader from '@/chat/components/ChatHeader.vue'
import ChatModeSwitcher from '@/chat/components/ChatModeSwitcher.vue'
import ChatInputBar from '@/chat/components/ChatInputBar.vue'
import ChatHistoryDrawer from '@/chat/components/ChatHistoryDrawer.vue'
import DeptPickerPopup from '@/chat/components/DeptPickerPopup.vue'
import { useBrandStore } from '@/stores/brand'

const router = useRouter()
const { brand } = storeToRefs(useBrandStore())
/** 助手称呼：配置了单位名称则落款到机构（{单位名称}智能助手），否则回退「智能健康助手」 */
const assistantName = computed(() =>
  brand.value.org_name ? `${brand.value.org_name}智能助手` : '智能健康助手',
)
const { departments, selectedDepartmentId, activeDepartment, selectDepartment } = useDepartments({ initialDepartmentId: 0, includeAll: false })

/** 知识库模式才渲染，异步加载避免首屏白拉 markdown-it（~98KB）。 */
const KnowledgeList = defineAsyncComponent(() => import('@/chat/views/KnowledgeList.vue'))

const { showToast } = useDsToast()
/** 提示当前生效的知识库；科室未加载完成时静默，等 name watcher 首次就绪再提示 */
function notifyCurrentDept() {
  const name = activeDepartment.value?.name
  if (name) showToast(`当前知识库：${name}`)
}

// 知识库提示改为一过式 toast：进入页面（科室加载完成）自动提示一次，切换科室后再提示一次。
// 监听 name 而非整个对象，避免科室数组被重新赋值时同样的名字重复弹窗。
watch(
 () => activeDepartment.value?.name,
 (name) => {
  if (name) notifyCurrentDept()
 },
)

const showHistory = ref(false)
const showDeptPicker = ref(false)

/** 视图模式：聊天 / 知识库 — 同步到 URL query，跨导航（如文章详情返回）可恢复 */
const { activeMode, setMode } = useChatMode()

// 从知识库切回聊天模式时，即使科室没有变化也提示一次当前知识库
watch(activeMode, (mode, prev) => {
  if (mode === 'chat' && prev === 'knowledge') notifyCurrentDept()
})

interface QuickAction {
 label: string
 value: string
 icon: Component
}

interface RecommendationTopic {
 label: string
 keywords: string[]
 icon: Component
}

/** 安全可答的推荐主题：只覆盖宣教与就医指征，不含用药/诊断/急救。 */
const RECOMMENDATION_TOPICS: RecommendationTopic[] = [
 { label: '高血压患者饮食要注意什么', keywords: ['高血压'], icon: HeartPulse },
 { label: '糖尿病日常怎么监测', keywords: ['糖尿病'], icon: Droplet },
 { label: '术后康复要注意什么', keywords: ['术后', '手术'], icon: Activity },
 { label: '出现哪些情况需要及时就医', keywords: ['就医', '就诊', '复查'], icon: HeartPulse },
]
/** 推荐位最多展示 3 条，避免挤压首屏输入区。 */
const MAX_RECOMMENDATIONS = 3
/** 扫描当前科室已发布文章，用于判断推荐主题是否有知识库来源。 */
const RECOMMENDATION_SCAN_SIZE = 50
/** 命中这些词的标题不进入推荐位：避免把用户引导到受限的用药/诊断话题。 */
const RESTRICTED_TITLE_RE = /用药|药物|剂量|停药|处方|诊断|确诊|急救|抢救/

const recommendedQuestions = ref<QuickAction[]>([])
const recommendationsLoading = ref(true)
const recommendationsEmpty = ref(false)
/** 未聚焦具体科室（id=0）时不要误称“当前科室”。 */
const recommendationEmptyText = computed(() =>
 selectedDepartmentId.value > 0 ? '当前科室知识库暂无可用推荐内容' : '知识库暂无可用推荐内容',
)

/** 请求序号：切换科室时丢弃旧科室的迟到响应。 */
let recommendationSeq = 0

/**
 * 推荐问题必须“当前知识库可答”：
 * 1. 按当前科室拉取已发布文章；
 * 2. 命中安全主题才展示，最多 3 条；
 * 3. 主题都没命中时，退化为基于真实文章标题的引导问题；
 * 4. 知识库为空/接口失败时隐藏推荐，改为“切换科室”提示，避免展示必然被拒的问题。
 */
async function loadRecommendations() {
 const seq = ++recommendationSeq
 recommendationsLoading.value = true
 try {
  const res = await wikiApi.listArticles({
   department_id: selectedDepartmentId.value > 0 ? selectedDepartmentId.value : undefined,
   page_size: RECOMMENDATION_SCAN_SIZE,
  })
  if (seq !== recommendationSeq) return
  const safeArticles = res.items.filter((a) => !RESTRICTED_TITLE_RE.test(a.title))
  const picked: QuickAction[] = []
  for (const topic of RECOMMENDATION_TOPICS) {
   if (picked.length >= MAX_RECOMMENDATIONS) break
   const matched = safeArticles.some((a) =>
    topic.keywords.some((k) => a.title.includes(k) || a.summary.includes(k)),
   )
   if (matched) picked.push({ label: topic.label, value: topic.label, icon: topic.icon })
  }
  if (picked.length === 0) {
   for (const article of safeArticles.slice(0, MAX_RECOMMENDATIONS)) {
    picked.push({
     label: article.title,
     value: `请介绍「${article.title}」中患者需要注意什么`,
     icon: BookOpen,
    })
   }
  }
  recommendedQuestions.value = picked
  recommendationsEmpty.value = picked.length === 0
 } catch {
  if (seq !== recommendationSeq) return
  recommendedQuestions.value = []
  recommendationsEmpty.value = true
 } finally {
  if (seq === recommendationSeq) recommendationsLoading.value = false
 }
}

watch(selectedDepartmentId, () => { void loadRecommendations() }, { immediate: true })

function openHistory() {
 showHistory.value = true
}

function onHistorySelect(conversationId: string) {
 router.push({ name: 'chat-conversation', params: { id: conversationId } })
}

function onHistoryNewChat() {
  // 已在首页即处于全新对话；仅关闭抽屉，避免「新对话」按钮后抽屉残留打开
  showHistory.value = false
}

function openDeptPicker() {
 showDeptPicker.value = true
 // 打开科室选择器即提示当前知识库，便于确认当前生效范围（不依赖是否有值变化）
 notifyCurrentDept()
}

function onDeptSelect(id: number) {
 const unchanged = id === selectedDepartmentId.value
 selectDepartment(id)
 // 选择同一科室不会触发 name watcher，这里补一次提示
 if (unchanged) notifyCurrentDept()
}

function sendMessage(text: string) {
 router.push({
 name: 'chat-conversation',
 params: {},
 query: { q: text, department: String(selectedDepartmentId.value) },
 })
}

function onPromptClick(item: QuickAction) {
 sendMessage(item.value)
}
</script>

<template>
 <div class="chat-home">
 <!-- 顶部栏 -->
 <ChatHeader variant="frosted" @open-history="openHistory">
 <template #center>
 <ChatModeSwitcher :active-mode="activeMode" @update:mode="setMode" />
 </template>
 </ChatHeader>

 <!-- 聊天模式 -->
 <div v-if="activeMode === 'chat'" class="chat-home-body px-[var(--spacer-20)]">
 <!-- Hero — 现代化欢迎区：留白 + 轻法式问候 + 呼吸感 orb -->
 <section class="flex flex-col items-center text-center pt-[var(--spacer-32)] pb-[var(--spacer-32)]">
 <div class="ai-orb-wrapper" aria-hidden="true">
 <div class="ai-orb-pulse-outer" />
 <div class="ai-orb-pulse-inner" />
 <div class="ai-orb flex items-center justify-center">
 <Sparkles class="w-9 h-9 text-onbrand" />
 </div>
 </div>

 <h2 class="hero-headline text-center font-semibold leading-[1.3] text-text break-words text-[var(--hero-headline-size)] mt-[var(--spacer-24)]">
 您好，我是您的<span class="hero-headline-accent">{{ assistantName }}</span>
 </h2>
 <p class="text-center text-body-base text-text-secondary max-w-[300px] leading-[1.6] mt-[var(--spacer-12)]">
 7×24 小时问答，答案可溯源
 </p>
 </section>

 <!-- 向我提问 - 仅展示当前知识库可答的安全主题 -->
 <section class="pb-[var(--spacer-20)]">
 <div v-if="recommendedQuestions.length > 0" class="flex flex-col gap-[var(--spacer-10)]">
 <button
 v-for="q in recommendedQuestions"
 :key="q.label"
 class="ai-context-card group flex items-center w-full text-left bg-[var(--bg-base-default)] rounded-[var(--radius-16)] px-[var(--spacer-16)] py-[var(--spacer-14,14px)] gap-[var(--spacer-12)] shadow-[var(--shadow-xs)] ring-1 ring-[var(--border-neutral-l1)]"
 @click="onPromptClick(q)"
 >
 <span class="ai-context-icon shrink-0 flex items-center justify-center w-11 h-11 rounded-[var(--radius-12)] bg-[var(--bg-brand-light)] text-icon-brand">
 <component :is="q.icon" :size="22" />
 </span>
 <span class="flex-1 min-w-0 text-body-base text-text font-medium leading-snug">{{ q.label }}</span>
 <ChevronRight :size="18" class="shrink-0 text-icon-tertiary transition-transform duration-200 group-hover:translate-x-0.5" />
 </button>
 </div>
 <div
 v-else-if="!recommendationsLoading && recommendationsEmpty"
 class="flex items-center gap-[var(--spacer-8)] rounded-[var(--radius-12)] bg-[var(--bg-base-secondary)] px-[var(--spacer-16)] py-[var(--spacer-12)] text-body-sm text-text-secondary"
 >
 <span class="flex-1">{{ recommendationEmptyText }}</span>
 <button type="button" class="shrink-0 font-medium text-text-brand" @click="openDeptPicker">切换科室</button>
 </div>
 </section>

 <!-- 免责声明 -->
 <DisclaimerFooter />
 </div>

 <!-- 输入卡片 - 悬浮底部 -->
 <ChatInputBar
 v-if="activeMode === 'chat'"
 class="chat-home-input-bar"
 :department-name="activeDepartment?.name"
 @send="sendMessage"
 @open-dept-picker="openDeptPicker"
 />

 <!-- 知识库模式 -->
 <KnowledgeList v-else embedded />

 <!-- 历史抽屉 -->
 <ChatHistoryDrawer
 v-model:visible="showHistory"
 @select="onHistorySelect"
 @new-chat="onHistoryNewChat"
 />

 <!-- 科室选择弹窗 -->
 <DeptPickerPopup
 v-model:show="showDeptPicker"
 :departments="departments"
 :selected-id="selectedDepartmentId"
 @select="onDeptSelect"
 />
 </div>
</template>
