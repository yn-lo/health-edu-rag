<script setup lang="ts">
/**
 * 跨科室引用管理 — 公开文章直接引用版
 * API: wikiApi.applyReference/listReferences/revokeReference
 * 设计原则：移动优先、引用申请需源科室管理员审批、源文章变动提示
 */
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import {
  Plus, Ban, Search, ArrowRightLeft,
  FileText, Building2, Clock, User,
  AlertTriangle, Check, X,
} from '@lucide/vue'
import { useDsToast, useDsDialog } from '@/shared/composables'
import { AppHeader, PageShell, EmptyState, DsPopup } from '@/shared/components'
import { wikiApi, fmtDateTime, timeAgo } from '@/shared'
import { errmsg } from '@/shared/api/client'
import { useAuthStore } from '@/stores/auth'
import { ADMIN_ROLES, SUPER_ADMIN_ROLE } from '@/shared/constants/roles'
import type { ArticleReference, ReferenceStatus, ArticlePublic } from '@/shared'

const router = useRouter()
const authStore = useAuthStore()
const { showSuccessToast, showFailToast } = useDsToast()
const { showConfirmDialog } = useDsDialog()

/** 仅科室管理员/超管可审批、撤销（后端 assertCanReviewReference 强校验）。 */
const canReview = computed(() => Boolean(authStore.user && ADMIN_ROLES.includes(authStore.user.role)))
/** 超管无主科室：后端不按方向过滤，UI 需展示双向信息并隐藏方向统计。 */
const isSuperAdmin = computed(() => authStore.user?.role === SUPER_ADMIN_ROLE)

// ===== 列表数据 =====
const references = ref<ArticleReference[]>([])
const total = ref(0)
const loading = ref(false)
const search = ref('')
const filterStatus = ref<'pending' | 'approved' | 'rejected' | 'revoked' | 'all'>('all')
const filterDirection = ref<'outgoing' | 'incoming'>('outgoing')
const page = ref(1)
const PAGE_SIZE = 20

// 搜索走后端过滤（覆盖全部分页）；防抖避免逐字请求。
let searchTimer: number | undefined
watch(search, () => {
  window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(() => {
    page.value = 1
    void load()
  }, 300)
})
onUnmounted(() => window.clearTimeout(searchTimer))

// ===== 统计 =====
const approvedCount = ref(0)
const incomingCount = ref(0)

// ===== 状态胶囊 =====
const statusChips: { value: 'pending' | 'approved' | 'rejected' | 'revoked' | 'all'; label: string }[] = [
  { value: 'all', label: '全部' },
  { value: 'pending', label: '待审批' },
  { value: 'approved', label: '已引用' },
  { value: 'rejected', label: '已驳回' },
  { value: 'revoked', label: '已撤销' },
]

const statusLabel: Record<string, string> = {
  pending: '待审批',
  approved: '已引用',
  rejected: '已驳回',
  revoked: '已撤销',
}

const statusTagVariant: Record<string, string> = {
  pending: 'ds-tag--warning',
  approved: 'ds-tag--success',
  rejected: 'ds-tag--default',
  revoked: 'ds-tag--default',
}

/** 源文章状态提示文案 */
function sourceArticleWarning(status: string): string {
  switch (status) {
    case 'archived': return '源文章已归档'
    case 'deleted': return '源文章已删除'
    case 'draft': return '源文章已变为草稿'
    case 'pending': return '源文章待审核'
    default: return ''
  }
}

// ===== 加载列表 =====
/** 请求序号：搜索/翻页/筛选并发时丢弃过期响应，避免旧页覆盖新页。 */
let loadSeq = 0
async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const params: {
      status?: ReferenceStatus
      direction: 'outgoing' | 'incoming'
      search?: string
      page: number
      page_size: number
    } = { direction: filterDirection.value, page: page.value, page_size: PAGE_SIZE }
    if (filterStatus.value !== 'all') params.status = filterStatus.value
    if (search.value.trim()) params.search = search.value.trim()
    const res = await wikiApi.listReferences(params)
    if (seq !== loadSeq) return
    references.value = res.items
    total.value = res.total
  } catch (e) {
    if (seq === loadSeq) showFailToast(errmsg(e, '加载失败'))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

/** 加载统计（超管无方向语义，跳过无效请求） */
async function loadStats() {
  if (isSuperAdmin.value) return
  try {
    const [outRes, inRes] = await Promise.all([
      wikiApi.listReferences({ direction: 'outgoing', status: 'approved', page: 1, page_size: 1 }),
      wikiApi.listReferences({ direction: 'incoming', status: 'approved', page: 1, page_size: 1 }),
    ])
    approvedCount.value = outRes.total
    incomingCount.value = inRes.total
  } catch {
    // 统计加载失败不阻塞主流程
  }
}

function onDirectionChange(dir: 'outgoing' | 'incoming') {
  filterDirection.value = dir
  filterStatus.value = 'all'
  page.value = 1
  load()
}

function onStatusChange(status: 'pending' | 'approved' | 'rejected' | 'revoked' | 'all') {
  filterStatus.value = status
  page.value = 1
  load()
}

// ===== 引用文章（点击即引用） =====
const showArticlePicker = ref(false)
const articleSearch = ref('')
const publicArticles = ref<ArticlePublic[]>([])
const articlesLoading = ref(false)
const applying = ref(false)

function openArticlePicker() {
  articleSearch.value = ''
  showArticlePicker.value = true
  loadPublicArticles()
}

/** 加载公开可引用的文章（其他科室的 allow_reference=true 文章） */
async function loadPublicArticles() {
  articlesLoading.value = true
  try {
    const res = await wikiApi.listReferenceableArticles({ page: 1, page_size: 100 })
    publicArticles.value = res.items
  } catch {
    publicArticles.value = []
  } finally {
    articlesLoading.value = false
  }
}

const filteredArticles = computed(() => {
  const q = articleSearch.value.trim().toLowerCase()
  if (!q) return publicArticles.value
  return publicArticles.value.filter(
    (a) => a.title.toLowerCase().includes(q) || a.department_name.toLowerCase().includes(q),
  )
})

/** 选择文章后直接引用 */
async function selectAndApply(a: ArticlePublic) {
  const deptId = authStore.user?.dept_id
  if (!deptId) {
    showFailToast('无法获取您的科室信息，请重新登录')
    return
  }
  try {
    await showConfirmDialog({
      title: '引用文章',
      message: `将「${a.title}」引用到本科室知识库？`,
      confirmButtonText: '引用',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  applying.value = true
  try {
    await wikiApi.applyReference({
      article_id: a.id,
      target_dept_id: deptId,
    })
    showSuccessToast('引用申请已提交，等待源科室审批')
    showArticlePicker.value = false
    await Promise.all([load(), loadStats()])
  } catch (e) {
    showFailToast(errmsg(e, '引用失败'))
  } finally {
    applying.value = false
  }
}

// ===== 撤销 =====
async function revoke(r: ArticleReference) {
  try {
    await showConfirmDialog({
      title: '撤销引用',
      message: `撤销「${r.article_title}」对「${r.target_dept_name}」的引用授权？撤销后该科室将无法继续引用。`,
      confirmButtonText: '撤销',
      danger: true,
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await wikiApi.revokeReference(r.id)
    showSuccessToast('已撤销')
    await Promise.all([load(), loadStats()])
  } catch (e) {
    showFailToast(errmsg(e, '操作失败'))
  }
}

// ===== 审批（仅源科室管理员可操作，后端强校验） =====
async function approve(r: ArticleReference) {
  try {
    await showConfirmDialog({
      title: '通过引用申请',
      message: `允许「${r.target_dept_name}」引用「${r.article_title}」？`,
      confirmButtonText: '通过',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await wikiApi.approveReference(r.id)
    showSuccessToast('已通过')
    await Promise.all([load(), loadStats()])
  } catch (e) {
    showFailToast(errmsg(e, '操作失败'))
  }
}

async function reject(r: ArticleReference) {
  try {
    await showConfirmDialog({
      title: '驳回引用申请',
      message: `驳回「${r.target_dept_name}」对「${r.article_title}」的引用申请？`,
      confirmButtonText: '驳回',
      danger: true,
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    // 后端 reason 必填：使用固定原因，避免弹出输入组件破坏当前交互。
    await wikiApi.rejectReference(r.id, '源科室未同意引用')
    showSuccessToast('已驳回')
    await Promise.all([load(), loadStats()])
  } catch (e) {
    showFailToast(errmsg(e, '操作失败'))
  }
}

// ===== 详情弹窗 =====
const showDetail = ref(false)
const detailRef = ref<ArticleReference | null>(null)

function viewDetail(r: ArticleReference) {
  detailRef.value = r
  showDetail.value = true
}

// ===== 分页 =====
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))
function goPage(p: number) {
  if (p < 1 || p > totalPages.value) return
  page.value = p
  load()
}

onMounted(() => {
  load()
  loadStats()
})
</script>

<template>
  <PageShell :bottom-nav="false">
    <AppHeader title="跨科室引用" @back="router.back">
      <template #right>
        <button
          v-if="!isSuperAdmin"
          type="button"
          class="ds-icon-btn ds-icon-btn--sm ds-icon-btn--brand"
          aria-label="引用文章"
          @click="openArticlePicker"
        >
          <Plus class="icon h-5 w-5" />
        </button>
      </template>
    </AppHeader>

    <!-- 统计概览（超管无方向语义，改为显示总数） -->
    <div class="px-[var(--spacer-16)] pt-[var(--spacer-16)]">
      <div v-if="isSuperAdmin" class="rounded-[var(--radius-card-medium)] bg-[var(--bg-brand-light)] p-[var(--spacer-16)] text-body-md text-text-brand">
        共 {{ total }} 条{{ search || filterStatus !== 'all' ? '（当前筛选）' : '引用记录' }}
      </div>
      <div v-else class="grid grid-cols-2 gap-[var(--spacer-12)]">
        <button
          type="button"
          class="flex items-center gap-[var(--spacer-12)] rounded-[var(--radius-card-medium)] p-[var(--spacer-16)] text-left transition-colors"
          :class="filterDirection === 'outgoing'
            ? 'bg-[var(--bg-brand)] shadow-[var(--shadow-brand)]'
            : 'bg-[var(--bg-brand-light)]'"
          @click="onDirectionChange('outgoing')"
        >
          <span
            class="inline-flex items-center justify-center shrink-0 w-9 h-9 rounded-full"
            :class="filterDirection === 'outgoing' ? 'bg-[var(--text-onbrand)]' : 'bg-[var(--bg-brand)]'"
          >
            <ArrowRightLeft
              class="w-5 h-5"
              :class="filterDirection === 'outgoing' ? 'text-icon-brand' : 'text-icon-onbrand'"
            />
          </span>
          <div class="min-w-0">
            <div
              class="font-metric text-heading-xl leading-none font-semibold tabular-nums"
              :class="filterDirection === 'outgoing' ? 'text-onbrand' : 'text-text-brand'"
            >{{ approvedCount }}</div>
            <div
              class="mt-[var(--spacer-4)] text-body-sm"
              :class="filterDirection === 'outgoing' ? 'text-onbrand opacity-75' : 'text-text-secondary'"
            >被引用的</div>
          </div>
        </button>
        <button
          type="button"
          class="flex items-center gap-[var(--spacer-12)] rounded-[var(--radius-card-medium)] p-[var(--spacer-16)] text-left transition-colors"
          :class="filterDirection === 'incoming'
            ? 'bg-[var(--bg-brand)] shadow-[var(--shadow-brand)]'
            : 'bg-[var(--bg-brand-light)]'"
          @click="onDirectionChange('incoming')"
        >
          <span
            class="inline-flex items-center justify-center shrink-0 w-9 h-9 rounded-full"
            :class="filterDirection === 'incoming' ? 'bg-[var(--text-onbrand)]' : 'bg-[var(--bg-brand)]'"
          >
            <FileText
              class="w-5 h-5"
              :class="filterDirection === 'incoming' ? 'text-icon-brand' : 'text-icon-onbrand'"
            />
          </span>
          <div class="min-w-0">
            <div
              class="font-metric text-heading-xl leading-none font-semibold tabular-nums"
              :class="filterDirection === 'incoming' ? 'text-onbrand' : 'text-text-brand'"
            >{{ incomingCount }}</div>
            <div
              class="mt-[var(--spacer-4)] text-body-sm"
              :class="filterDirection === 'incoming' ? 'text-onbrand opacity-75' : 'text-text-secondary'"
            >我引用的</div>
          </div>
        </button>
      </div>
    </div>

    <!-- 搜索 + 筛选 -->
    <section class="px-[var(--spacer-16)] pt-[var(--spacer-16)]">
      <div class="ds-search-box ds-search-box--md">
        <Search class="ds-search-box__icon h-4 w-4" />
        <input
          v-model="search"
          type="text"
          placeholder="搜索文章 / 科室"
          class="ds-search-box__input"
        >
      </div>

      <!-- 状态胶囊 -->
      <div class="flex flex-wrap gap-[var(--spacer-8)] mt-[var(--spacer-12)]">
        <button
          v-for="opt in statusChips"
          :key="opt.value"
          type="button"
          class="inline-flex items-center h-8 px-[var(--spacer-12)] rounded-[var(--radius-full)] border text-body-sm font-medium whitespace-nowrap transition-colors"
          :class="filterStatus === opt.value
            ? 'border-[var(--brand-glow-border-strong)] bg-[var(--bg-brand-light)] text-text-brand'
            : 'border-[var(--border-neutral-l1)] bg-transparent text-text-secondary hover:bg-[var(--bg-overlay-l1)] hover:text-text'"
          @click="onStatusChange(opt.value)"
        >{{ opt.label }}</button>
      </div>
    </section>

    <!-- 引用列表 -->
    <section class="px-[var(--spacer-16)] pt-[var(--spacer-16)] pb-[var(--spacer-16)]">
      <div v-if="references.length === 0 && loading" class="py-[var(--spacer-32)] text-center text-body-md text-text-tertiary">
        加载中...
      </div>
      <EmptyState v-else-if="references.length === 0" :text="search ? '没有匹配的引用记录' : (filterDirection === 'outgoing' ? '暂无被引用记录' : '暂无引用记录，点击右上角 + 引用公开文章')" />
      <div v-else class="ds-list rounded-[var(--radius-card-large)] bg-[var(--bg-base-default)] overflow-hidden">
        <article
          v-for="r in references"
          :key="r.id"
          class="ds-list-item ds-list-item--divider"
          @click="viewDetail(r)"
        >
          <span
            class="ds-list-item__icon"
            :class="r.status === 'approved' ? 'ds-list-item__icon--success' : ''"
          >
            <ArrowRightLeft :size="20" />
          </span>
          <div class="ds-list-item__content">
            <span class="ds-list-item__title">
              {{ r.article_title }}
              <!-- 源文章变动提示 -->
              <span
                v-if="r.status === 'approved' && r.source_article_status && r.source_article_status !== 'published'"
                class="inline-flex items-center gap-[var(--spacer-4)] ml-[var(--spacer-4)] text-[var(--status-warning-default)]"
              >
                <AlertTriangle :size="14" />
                <span class="text-body-xs">{{ sourceArticleWarning(r.source_article_status) }}</span>
              </span>
            </span>
            <span class="ds-list-item__meta">
              <template v-if="isSuperAdmin">
                <span class="text-text-brand font-medium">{{ r.source_dept_name }}</span>
                <span>→</span>
                <span class="text-text-brand font-medium">{{ r.target_dept_name }}</span>
              </template>
              <template v-else-if="filterDirection === 'outgoing'">
                <span class="text-text-brand font-medium">{{ r.target_dept_name }}</span>
                <span>引用了本文</span>
              </template>
              <template v-else>
                <span>引用自</span>
                <span class="text-text-brand font-medium">{{ r.source_dept_name }}</span>
              </template>
              <span>· {{ r.applicant_name }}</span>
              <span>· {{ timeAgo(r.created_at) }}</span>
            </span>
          </div>
          <div class="ds-list-item__trailing" @click.stop>
            <span class="ds-tag ds-tag--plain" :class="statusTagVariant[r.status] ?? 'ds-tag--default'">{{ statusLabel[r.status] ?? r.status }}</span>
            <template v-if="canReview && r.status === 'pending' && (isSuperAdmin || filterDirection === 'outgoing')">
              <button
                type="button"
                class="ds-list-item__action-btn"
                aria-label="通过"
                @click="approve(r)"
              >
                <Check :size="16" />
              </button>
              <button
                type="button"
                class="ds-list-item__action-btn"
                aria-label="驳回"
                @click="reject(r)"
              >
                <X :size="16" />
              </button>
            </template>
            <button
              v-else-if="canReview && r.status === 'approved' && (isSuperAdmin || filterDirection === 'outgoing')"
              type="button"
              class="ds-list-item__action-btn"
              aria-label="撤销"
              @click="revoke(r)"
            >
              <Ban :size="16" />
            </button>
          </div>
        </article>
      </div>

      <!-- 分页 -->
      <div v-if="totalPages > 1" class="mt-[var(--spacer-12)] flex items-center justify-center gap-[var(--spacer-8)]">
        <button
          type="button"
          class="ds-btn ds-btn--ghost ds-btn--sm"
          :disabled="page <= 1"
          @click="goPage(page - 1)"
        >上一页</button>
        <span class="text-body-sm text-text-tertiary">{{ page }} / {{ totalPages }}</span>
        <button
          type="button"
          class="ds-btn ds-btn--ghost ds-btn--sm"
          :disabled="page >= totalPages"
          @click="goPage(page + 1)"
        >下一页</button>
      </div>
    </section>

    <!-- ===== 详情弹窗 ===== -->
    <DsPopup v-model:show="showDetail">
      <div v-if="detailRef" class="p-[var(--spacer-16)] pb-[var(--spacer-24)]">
        <!-- 标题区 -->
        <div class="flex items-start gap-[var(--spacer-12)] mb-[var(--spacer-16)]">
          <span
            class="inline-flex items-center justify-center shrink-0 w-10 h-10 rounded-[var(--radius-8)]"
            :class="detailRef.status === 'approved' ? 'bg-[var(--status-success-light)]' : 'bg-[var(--bg-overlay-l1)]'"
          >
            <ArrowRightLeft
              class="w-5 h-5"
              :class="detailRef.status === 'approved' ? 'text-[var(--status-success-default)]' : 'text-icon'"
            />
          </span>
          <div class="min-w-0 flex-1">
            <h3 class="m-0 font-heading text-heading-sm font-semibold text-text leading-tight">{{ detailRef.article_title }}</h3>
            <span class="mt-[var(--spacer-4)] inline-block ds-tag ds-tag--plain" :class="statusTagVariant[detailRef.status] ?? 'ds-tag--default'">{{ statusLabel[detailRef.status] ?? detailRef.status }}</span>
          </div>
        </div>

        <!-- 源文章变动提示 -->
        <div
          v-if="detailRef.status === 'approved' && detailRef.source_article_status && detailRef.source_article_status !== 'published'"
          class="flex items-center gap-[var(--spacer-8)] mb-[var(--spacer-12)] px-[var(--spacer-12)] py-[var(--spacer-8)] rounded-[var(--radius-8)] bg-[var(--status-warning-light)]"
        >
          <AlertTriangle class="shrink-0 w-4 h-4 text-[var(--status-warning-default)]" />
          <span class="text-body-sm text-[var(--status-warning-default)]">{{ sourceArticleWarning(detailRef.source_article_status) }}，引用可能已失效</span>
        </div>

        <!-- 信息网格 -->
        <div class="flex flex-col gap-[var(--spacer-12)] rounded-[var(--radius-8)] bg-[var(--bg-base-secondary)] p-[var(--spacer-12)]">
          <div class="flex items-center gap-[var(--spacer-8)]">
            <Building2 class="shrink-0 w-4 h-4 text-icon-tertiary" />
            <span class="text-body-sm text-text-tertiary">源科室</span>
            <span class="ml-auto text-body-sm font-medium text-text">{{ detailRef.source_dept_name }}</span>
          </div>
          <div class="flex items-center gap-[var(--spacer-8)]">
            <Building2 class="shrink-0 w-4 h-4 text-icon-tertiary" />
            <span class="text-body-sm text-text-tertiary">目标科室</span>
            <span class="ml-auto text-body-sm font-medium text-text">{{ detailRef.target_dept_name }}</span>
          </div>
          <div class="flex items-center gap-[var(--spacer-8)]">
            <User class="shrink-0 w-4 h-4 text-icon-tertiary" />
            <span class="text-body-sm text-text-tertiary">操作人</span>
            <span class="ml-auto text-body-sm text-text">{{ detailRef.applicant_name }}</span>
          </div>
          <div class="flex items-center gap-[var(--spacer-8)]">
            <Clock class="shrink-0 w-4 h-4 text-icon-tertiary" />
            <span class="text-body-sm text-text-tertiary">引用时间</span>
            <span class="ml-auto text-body-sm text-text">{{ fmtDateTime(detailRef.created_at) }}</span>
          </div>
        </div>

        <!-- 操作按钮 -->
        <div v-if="canReview && detailRef.status === 'pending' && (isSuperAdmin || filterDirection === 'outgoing')" class="mt-[var(--spacer-16)] flex gap-[var(--spacer-8)]">
          <button type="button" class="ds-btn ds-btn--primary ds-btn--block" @click="showDetail = false; approve(detailRef!)">通过</button>
          <button type="button" class="ds-btn ds-btn--danger-outline ds-btn--block" @click="showDetail = false; reject(detailRef!)">驳回</button>
        </div>
        <div v-else-if="canReview && detailRef.status === 'approved' && (isSuperAdmin || filterDirection === 'outgoing')" class="mt-[var(--spacer-16)]">
          <button type="button" class="ds-btn ds-btn--danger-outline ds-btn--block" @click="showDetail = false; revoke(detailRef!)">撤销引用</button>
        </div>
        <div v-else class="mt-[var(--spacer-16)]">
          <button type="button" class="ds-btn ds-btn--secondary ds-btn--block" @click="showDetail = false">关闭</button>
        </div>
      </div>
    </DsPopup>

    <!-- ===== 文章选择弹窗（点击即引用） ===== -->
    <DsPopup v-model:show="showArticlePicker">
      <div class="flex flex-col max-h-[70vh]">
        <header class="px-[var(--spacer-16)] pt-[var(--spacer-16)] pb-[var(--spacer-12)]">
          <h3 class="m-0 text-heading-sm font-semibold text-text">选择公开文章</h3>
          <p class="m-0 mt-[var(--spacer-4)] text-body-xs text-text-tertiary">点击文章即可引用到本科室知识库</p>
        </header>
        <div class="px-[var(--spacer-16)] pb-[var(--spacer-8)]">
          <div class="ds-search-box">
            <Search class="ds-search-box__icon h-4 w-4" />
            <input
              v-model="articleSearch"
              type="text"
              placeholder="搜索文章标题或科室"
              class="ds-search-box__input"
            >
          </div>
        </div>
        <div v-if="articlesLoading" class="py-[var(--spacer-24)] text-center text-body-sm text-text-tertiary">
          加载中...
        </div>
        <div v-else-if="filteredArticles.length === 0" class="py-[var(--spacer-24)] text-center text-body-sm text-text-tertiary">
          暂无公开可引用文章
        </div>
        <ul v-else class="flex-1 overflow-y-auto list-none m-0 p-0 no-scrollbar">
          <li
            v-for="a in filteredArticles"
            :key="a.id"
            class="flex items-center gap-[var(--spacer-12)] px-[var(--spacer-16)] py-[var(--spacer-12)] border-b border-[var(--border-neutral-l1)] transition-colors active:bg-[var(--bg-overlay-l1)]"
            @click="selectAndApply(a)"
          >
            <span class="inline-flex items-center justify-center shrink-0 w-8 h-8 rounded-[var(--radius-4)] bg-[var(--bg-overlay-l1)]">
              <FileText class="w-4 h-4 text-icon" />
            </span>
            <div class="min-w-0 flex-1">
              <div class="truncate text-body-base font-medium text-text">{{ a.title }}</div>
              <div class="text-body-xs text-text-tertiary">{{ a.department_name }}</div>
            </div>
            <Plus v-if="!applying" class="shrink-0 w-5 h-5 text-icon-brand" />
          </li>
        </ul>
        <div class="px-[var(--spacer-16)] py-[var(--spacer-12)]">
          <button type="button" class="ds-btn ds-btn--secondary ds-btn--block" @click="showArticlePicker = false">
            取消
          </button>
        </div>
      </div>
    </DsPopup>
  </PageShell>
</template>
